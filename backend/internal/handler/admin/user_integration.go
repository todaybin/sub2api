package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type integrationKeyService interface {
	Create(context.Context, int64, service.CreateAPIKeyRequest) (*service.APIKey, error)
	GetByID(context.Context, int64) (*service.APIKey, error)
	GetAvailableGroups(context.Context, int64) ([]service.Group, error)
}
type integrationAuthService interface {
	GenerateTokenPair(context.Context, *service.User, string) (*service.TokenPair, error)
}
type integrationUsageService interface {
	GetStatsWithFilters(context.Context, usagestats.UsageLogFilters) (*usagestats.UsageStats, error)
}
type integrationModelService interface {
	SmartModelCatalog(context.Context, *service.Group) ([]string, error)
}

func (h *UserHandler) SetIntegrationServices(keys *service.APIKeyService, auth *service.AuthService, usage *service.UsageService, models *service.GatewayService) {
	h.integrationKeys, h.integrationAuth, h.integrationUsage, h.integrationModels = keys, auth, usage, models
}

func integrationUserID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, false
	}
	return id, true
}

// Resolve defaults at the integration boundary, leaving existing user CRUD defaults intact.
func resolveIntegrationKey(ctx context.Context, keys interface {
	GetAvailableGroups(context.Context, int64) ([]service.Group, error)
}, userID int64, req service.CreateAPIKeyRequest) (service.CreateAPIKeyRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.RoutingMode = strings.ToLower(strings.TrimSpace(req.RoutingMode))
	if req.Name == "" {
		return req, infraerrors.BadRequest("API_KEY_NAME_REQUIRED", "api key name is required")
	}
	if req.GroupID != nil {
		if *req.GroupID <= 0 || (req.RoutingMode != "" && req.RoutingMode != "single") || len(req.SmartGroupIDs) > 0 {
			return req, infraerrors.BadRequest("API_KEY_GROUP_CONFLICT", "group_id requires single routing without smart_group_ids")
		}
		req.RoutingMode = "single"
	} else {
		if req.RoutingMode != "" && req.RoutingMode != "smart" {
			return req, infraerrors.BadRequest("API_KEY_GROUP_REQUIRED", "single routing requires group_id")
		}
		req.RoutingMode = "smart"
		if len(req.SmartGroupIDs) == 0 {
			groups, err := keys.GetAvailableGroups(ctx, userID)
			if err != nil {
				return req, err
			}
			for _, group := range groups {
				req.SmartGroupIDs = append(req.SmartGroupIDs, group.ID)
			}
		}
		if len(req.SmartGroupIDs) == 0 {
			return req, infraerrors.BadRequest("API_KEY_SMART_GROUPS_REQUIRED", "user has no available groups")
		}
		for _, id := range req.SmartGroupIDs {
			if id <= 0 {
				return req, infraerrors.BadRequest("API_KEY_GROUP_INVALID", "smart_group_ids must be positive")
			}
		}
		req.SmartGroupIDs = mergeInt64Sets(nil, req.SmartGroupIDs)
	}
	if req.RoutingStrategy == "" {
		req.RoutingStrategy = "auto"
	}
	return req, nil
}

func (h *UserHandler) CreateUserAPIKey(c *gin.Context) {
	id, ok := integrationUserID(c)
	if !ok {
		return
	}
	var req ProvisioningAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if _, err := h.adminService.GetUser(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	executeAdminIdempotentJSON(c, "admin.users.api-keys.create", struct {
		UserID int64
		Body   ProvisioningAPIKeyRequest
	}{id, req}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		input, err := resolveIntegrationKey(ctx, h.integrationKeys, id, req.serviceRequest())
		if err != nil {
			return nil, err
		}
		key, err := h.integrationKeys.Create(ctx, id, input)
		if err != nil {
			return nil, err
		}
		return dto.APIKeyFromService(key), nil
	})
}

// A trusted admin may delegate only a regular active user's session, never an admin's session.
func (h *UserHandler) IssueUserToken(c *gin.Context) {
	id, ok := integrationUserID(c)
	if !ok {
		return
	}
	var req struct {
		ClientIP  string `json:"client_ip"`
		UserAgent string `json:"user_agent"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.ClientIP != "" && net.ParseIP(req.ClientIP) == nil {
		response.BadRequest(c, "client_ip must be an IP address")
		return
	}
	if (req.ClientIP == "") != (req.UserAgent == "") || len(req.UserAgent) > 512 {
		response.BadRequest(c, "client_ip and user_agent must be supplied together; user_agent maximum is 512 bytes")
		return
	}
	c.Header("Cache-Control", "no-store")
	user, err := h.adminService.GetUser(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if user.Role != service.RoleUser || !user.IsActive() {
		response.Forbidden(c, "Only active regular users may receive integration login tokens")
		return
	}
	if user.TotpEnabled {
		response.Forbidden(c, "User has two-factor authentication enabled; use the normal login flow")
		return
	}
	executeAdminIdempotentJSON(c, "admin.users.token.issue", struct {
		UserID              int64
		ClientIP, UserAgent string
	}{id, req.ClientIP, req.UserAgent}, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		// Bind to the end user if supplied, never to the integration server's network identity.
		ctx = service.WithSessionBinding(ctx, &service.SessionBinding{IP: req.ClientIP, UserAgent: req.UserAgent})
		pair, err := h.integrationAuth.GenerateTokenPair(ctx, user, "")
		if err != nil {
			return nil, err
		}
		slog.Info("admin.integration_user_token_issued", "actor", adminActorScope(c), "target_user_id", id)
		return gin.H{"user_id": id, "access_token": pair.AccessToken, "refresh_token": pair.RefreshToken, "expires_in": pair.ExpiresIn, "token_type": "Bearer"}, nil
	})
}

func integrationUsageRange(c *gin.Context) (time.Time, time.Time, error) {
	loc := time.UTC
	if name := c.Query("timezone"); name != "" {
		var err error
		loc, err = time.LoadLocation(name)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid timezone")
		}
	}
	start, end := c.Query("start_time"), c.Query("end_time")
	sd, ed := c.Query("start_date"), c.Query("end_date")
	if start != "" || end != "" {
		if start == "" || end == "" || sd != "" || ed != "" {
			return time.Time{}, time.Time{}, fmt.Errorf("provide both start_time and end_time, without date parameters")
		}
		s, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return s, s, fmt.Errorf("invalid start_time, use RFC3339")
		}
		e, err := time.Parse(time.RFC3339, end)
		if err != nil {
			return s, e, fmt.Errorf("invalid end_time, use RFC3339")
		}
		if !s.Before(e) {
			return s, e, fmt.Errorf("start_time must precede end_time")
		}
		return s, e, nil
	}
	if sd != "" || ed != "" {
		if sd == "" || ed == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("provide both start_date and end_date")
		}
		s, err := time.ParseInLocation("2006-01-02", sd, loc)
		if err != nil {
			return s, s, fmt.Errorf("invalid start_date, use YYYY-MM-DD")
		}
		e, err := time.ParseInLocation("2006-01-02", ed, loc)
		if err != nil {
			return s, e, fmt.Errorf("invalid end_date, use YYYY-MM-DD")
		}
		if e.Before(s) {
			return s, e, fmt.Errorf("start_date must not follow end_date")
		}
		return s, e.AddDate(0, 0, 1), nil
	}
	now := time.Now().In(loc)
	switch c.DefaultQuery("period", "month") {
	case "today":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), now, nil
	case "week":
		return now.AddDate(0, 0, -7), now, nil
	case "month":
		return now.AddDate(0, -1, 0), now, nil
	case "all":
		return time.Unix(0, 0).UTC(), now, nil
	default:
		return now, now, fmt.Errorf("period must be today, week, month, or all")
	}
}

func (h *UserHandler) integrationUserKey(c *gin.Context, userID int64) (*service.APIKey, error) {
	raw, present := c.GetQuery("api_key_id")
	if !present {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, infraerrors.BadRequest("API_KEY_ID_INVALID", "api_key_id must be positive")
	}
	key, err := h.integrationKeys.GetByID(c.Request.Context(), id)
	if err != nil {
		return nil, err
	}
	if key.UserID != userID {
		return nil, service.ErrAPIKeyNotFound
	}
	return key, nil
}

func (h *UserHandler) getIntegrationUserUsage(c *gin.Context, id int64) {
	if id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	start, end, err := integrationUsageRange(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if _, err := h.adminService.GetUser(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	key, err := h.integrationUserKey(c, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	filters := usagestats.UsageLogFilters{UserID: id, StartTime: &start, EndTime: &end}
	if key != nil {
		filters.APIKeyID = key.ID
	}
	stats, err := h.integrationUsage.GetStatsWithFilters(c.Request.Context(), filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, struct {
		*usagestats.UsageStats
		UserID    int64     `json:"user_id"`
		APIKeyID  int64     `json:"api_key_id,omitempty"`
		StartTime time.Time `json:"start_time"`
		EndTime   time.Time `json:"end_time"`
	}{stats, id, filters.APIKeyID, start.UTC(), end.UTC()})
}

// GetUserModels unions authorized groups, optionally scoped to one of the user's keys.
func (h *UserHandler) GetUserModels(c *gin.Context) {
	id, ok := integrationUserID(c)
	if !ok {
		return
	}
	if _, err := h.adminService.GetUser(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	groups, err := h.integrationKeys.GetAvailableGroups(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	key, err := h.integrationUserKey(c, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key != nil {
		ids := key.SmartGroupIDs
		if key.RoutingMode != "smart" {
			ids = nil
			if key.GroupID != nil {
				ids = []int64{*key.GroupID}
			}
		}
		allowed := map[int64]bool{}
		for _, id := range ids {
			allowed[id] = true
		}
		selected := []service.Group{}
		for _, g := range groups {
			if allowed[g.ID] {
				selected = append(selected, g)
			}
		}
		groups = selected
	}
	h.respondIntegrationModels(c, groups)
}

// GetGroupModels provides the union of active groups or an explicitly requested subset.
func (h *UserHandler) GetGroupModels(c *gin.Context) {
	groups, err := h.adminService.GetAllGroups(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	selected := map[int64]bool{}
	raw, present := c.GetQuery("group_ids")
	if present {
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id <= 0 {
				response.BadRequest(c, "group_ids must contain positive comma-separated IDs")
				return
			}
			selected[id] = true
		}
	}
	filtered := []service.Group{}
	for _, g := range groups {
		if !present || selected[g.ID] {
			filtered = append(filtered, g)
			delete(selected, g.ID)
		}
	}
	if len(selected) > 0 {
		response.NotFound(c, "Group not found")
		return
	}
	h.respondIntegrationModels(c, filtered)
}

func (h *UserHandler) respondIntegrationModels(c *gin.Context, groups []service.Group) {
	seen := map[string][]int64{}
	for _, group := range groups {
		ids, err := h.integrationModels.SmartModelCatalog(c.Request.Context(), &group)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				seen[id] = mergeInt64Sets(seen[id], []int64{group.ID})
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]gin.H, 0, len(names))
	for _, name := range names {
		models = append(models, gin.H{"id": name, "object": "model", "group_ids": seen[name]})
	}
	response.Success(c, gin.H{"object": "list", "models": models})
}
