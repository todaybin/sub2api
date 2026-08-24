package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const provisioningExternalIDPrefix = "integration_external_id:"

type provisioningAPIKeyService interface {
	Create(ctx context.Context, userID int64, req service.CreateAPIKeyRequest) (*service.APIKey, error)
	Delete(ctx context.Context, id int64, userID int64) error
}

// ProvisioningHandler 为可信集成幂等确保普通用户和模型分组 Key。
type ProvisioningHandler struct {
	adminService        service.AdminService
	apiKeyService       provisioningAPIKeyService
	subscriptionService *service.SubscriptionService
}

// NewProvisioningHandler 创建用户 Provision 处理器。
func NewProvisioningHandler(adminService service.AdminService, apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService) *ProvisioningHandler {
	return &ProvisioningHandler{adminService: adminService, apiKeyService: apiKeyService, subscriptionService: subscriptionService}
}

// ProvisioningAPIKeyRequest 描述需要确保存在的一个分组 Key。
type ProvisioningAPIKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	GroupID       *int64   `json:"group_id"`
	CustomKey     *string  `json:"custom_key"`
	IPWhitelist   []string `json:"ip_whitelist"`
	IPBlacklist   []string `json:"ip_blacklist"`
	Quota         float64  `json:"quota" binding:"gte=0"`
	ExpiresInDays *int     `json:"expires_in_days" binding:"omitempty,min=1,max=36500"`
	RateLimit5h   float64  `json:"rate_limit_5h" binding:"gte=0"`
	RateLimit1d   float64  `json:"rate_limit_1d" binding:"gte=0"`
	RateLimit7d   float64  `json:"rate_limit_7d" binding:"gte=0"`
}

// ProvisioningSubscriptionRequest 描述可选的首次订阅。
type ProvisioningSubscriptionRequest struct {
	GroupID      int64  `json:"group_id" binding:"required,gt=0"`
	ValidityDays int    `json:"validity_days" binding:"omitempty,min=1,max=36500"`
	Notes        string `json:"notes"`
}

// ProvisionUserRequest 同时兼容旧 api_key 和新的 api_keys 批量形式。
type ProvisionUserRequest struct {
	ExternalID    string                           `json:"external_id" binding:"omitempty,max=160"`
	Email         string                           `json:"email" binding:"required,email"`
	Password      string                           `json:"password" binding:"required,min=6"`
	Username      string                           `json:"username"`
	Notes         string                           `json:"notes"`
	Concurrency   int                              `json:"concurrency" binding:"gte=0"`
	RPMLimit      int                              `json:"rpm_limit" binding:"gte=0"`
	AllowedGroups []int64                          `json:"allowed_groups"`
	Balance       *float64                         `json:"balance" binding:"omitempty,gte=0"`
	APIKey        *ProvisioningAPIKeyRequest       `json:"api_key"`
	APIKeys       []ProvisioningAPIKeyRequest      `json:"api_keys"`
	Subscription  *ProvisioningSubscriptionRequest `json:"subscription"`
}

type provisioningAPIKeyResponse struct {
	ID             int64  `json:"id"`
	UserID         int64  `json:"user_id"`
	Name           string `json:"name"`
	GroupID        *int64 `json:"group_id"`
	Status         string `json:"status"`
	Key            string `json:"key,omitempty"`
	KeyFingerprint string `json:"key_fingerprint"`
	Created        bool   `json:"created"`
}

// Create 创建或认领普通用户，合并分组权限并只创建缺失的分组 Key。
func (h *ProvisioningHandler) Create(c *gin.Context) {
	var req ProvisionUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	keys, err := normalizeProvisionAPIKeys(req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if h.adminService == nil || h.apiKeyService == nil || h.subscriptionService == nil {
		response.Error(c, http.StatusInternalServerError, "Provisioning service unavailable")
		return
	}

	executeAdminIdempotentJSON(c, "admin.users.provision", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.ensureProvisioned(ctx, c, req, keys)
	})
}

func (h *ProvisioningHandler) ensureProvisioned(ctx context.Context, c *gin.Context, req ProvisionUserRequest, keyRequests []ProvisioningAPIKeyRequest) (any, error) {
	user, createdUser, previousUser, err := h.ensureUser(ctx, req, keyRequests, getAdminIDFromContext(c))
	if err != nil {
		return nil, err
	}
	committed := false
	createdKeyIDs := make([]int64, 0, len(keyRequests))
	defer func() {
		if committed {
			return
		}
		if createdUser {
			_ = h.adminService.DeleteUser(context.Background(), user.ID)
			return
		}
		for _, keyID := range createdKeyIDs {
			_ = h.apiKeyService.Delete(context.Background(), keyID, user.ID)
		}
		if previousUser != nil {
			groups := append([]int64(nil), previousUser.AllowedGroups...)
			notes := previousUser.Notes
			_, _ = h.adminService.UpdateUser(context.Background(), user.ID, &service.UpdateUserInput{
				AllowedGroups: &groups,
				Notes:         &notes,
				ActorAdminID:  getAdminIDFromContext(c),
			})
		}
	}()

	keyResponses, createdKeyIDs, err := h.ensureAPIKeys(ctx, user.ID, keyRequests)
	if err != nil {
		return nil, err
	}
	var subscription *service.UserSubscription
	if createdUser && req.Subscription != nil {
		subscription, err = h.subscriptionService.AssignSubscription(ctx, &service.AssignSubscriptionInput{
			UserID: user.ID, GroupID: req.Subscription.GroupID, ValidityDays: req.Subscription.ValidityDays,
			AssignedBy: getAdminIDFromContext(c), Notes: req.Subscription.Notes,
		})
		if err != nil {
			return nil, fmt.Errorf("assign subscription: %w", err)
		}
	}
	committed = true
	result := gin.H{
		"created":      createdUser,
		"external_id":  strings.TrimSpace(req.ExternalID),
		"user":         dto.UserFromServiceAdmin(user),
		"api_keys":     keyResponses,
		"subscription": dto.UserSubscriptionFromServiceAdmin(subscription),
	}
	if len(keyResponses) > 0 {
		// 旧调用方继续读取 api_key，新调用方使用 api_keys。
		result["api_key"] = keyResponses[0]
	}
	return result, nil
}

func (h *ProvisioningHandler) ensureUser(ctx context.Context, req ProvisionUserRequest, keyRequests []ProvisioningAPIKeyRequest, actorAdminID int64) (*service.User, bool, *service.User, error) {
	existing, err := h.findExistingUser(ctx, req.ExternalID, req.Email)
	if err != nil {
		return nil, false, nil, err
	}
	allowedGroups := mergedProvisionGroups(req.AllowedGroups, keyRequests)
	notes := notesWithExternalID(req.Notes, req.ExternalID)
	if existing == nil {
		user, createErr := h.adminService.CreateUser(ctx, &service.CreateUserInput{
			Email: req.Email, Password: req.Password, Username: req.Username, Notes: notes,
			Role: service.RoleUser, Balance: req.Balance, Concurrency: req.Concurrency,
			RPMLimit: req.RPMLimit, AllowedGroups: allowedGroups, ActorAdminID: actorAdminID,
		})
		return user, true, nil, createErr
	}
	if existing.Role != service.RoleUser {
		return nil, false, nil, infraerrors.Conflict("PROVISION_ACCOUNT_CONFLICT", "existing account is not a regular user")
	}
	previous := *existing
	previous.AllowedGroups = append([]int64(nil), existing.AllowedGroups...)
	merged := mergeInt64Sets(existing.AllowedGroups, allowedGroups)
	updates := &service.UpdateUserInput{ActorAdminID: actorAdminID}
	needsUpdate := !equalInt64Sets(existing.AllowedGroups, merged)
	if needsUpdate {
		updates.AllowedGroups = &merged
	}
	if strings.TrimSpace(req.ExternalID) != "" && !hasExternalID(existing.Notes, req.ExternalID) {
		existingNotes := notesWithExternalID(existing.Notes, req.ExternalID)
		updates.Notes = &existingNotes
		needsUpdate = true
	}
	if needsUpdate {
		updated, updateErr := h.adminService.UpdateUser(ctx, existing.ID, updates)
		if updateErr != nil {
			return nil, false, nil, fmt.Errorf("update provisioned user: %w", updateErr)
		}
		existing = updated
	}
	return existing, false, &previous, nil
}

func (h *ProvisioningHandler) findExistingUser(ctx context.Context, externalID, email string) (*service.User, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID != "" {
		users, _, err := h.adminService.ListUsers(ctx, 1, 100, service.UserListFilters{Search: externalID}, "id", "asc")
		if err != nil {
			return nil, fmt.Errorf("find user by external_id: %w", err)
		}
		matches := make([]service.User, 0, 1)
		for _, item := range users {
			if hasExternalID(item.Notes, externalID) {
				matches = append(matches, item)
			}
		}
		if len(matches) > 1 {
			return nil, infraerrors.Conflict("PROVISION_ACCOUNT_CONFLICT", "multiple users match external_id")
		}
		if len(matches) == 1 {
			return &matches[0], nil
		}
	}
	users, _, err := h.adminService.ListUsers(ctx, 1, 100, service.UserListFilters{Search: strings.TrimSpace(email)}, "id", "asc")
	if err != nil {
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	for _, item := range users {
		if strings.EqualFold(strings.TrimSpace(item.Email), strings.TrimSpace(email)) {
			return &item, nil
		}
	}
	return nil, nil
}

func (h *ProvisioningHandler) ensureAPIKeys(ctx context.Context, userID int64, requests []ProvisioningAPIKeyRequest) ([]provisioningAPIKeyResponse, []int64, error) {
	existing, _, err := h.adminService.GetUserAPIKeys(ctx, userID, 1, 1000, "id", "asc")
	if err != nil {
		return nil, nil, fmt.Errorf("list provisioned api keys: %w", err)
	}
	byGroup := make(map[string]service.APIKey, len(existing))
	for _, item := range existing {
		key := provisionGroupKey(item.GroupID)
		if _, ok := byGroup[key]; !ok {
			byGroup[key] = item
		}
	}
	responses := make([]provisioningAPIKeyResponse, 0, len(requests))
	createdIDs := make([]int64, 0, len(requests))
	for _, request := range requests {
		groupKey := provisionGroupKey(request.GroupID)
		if item, ok := byGroup[groupKey]; ok {
			responses = append(responses, provisioningKeyResponse(item, false))
			continue
		}
		created, createErr := h.apiKeyService.Create(ctx, userID, service.CreateAPIKeyRequest{
			Name: request.Name, GroupID: request.GroupID, CustomKey: request.CustomKey,
			IPWhitelist: request.IPWhitelist, IPBlacklist: request.IPBlacklist, Quota: request.Quota,
			ExpiresInDays: request.ExpiresInDays, RateLimit5h: request.RateLimit5h,
			RateLimit1d: request.RateLimit1d, RateLimit7d: request.RateLimit7d,
		})
		if createErr != nil {
			return nil, createdIDs, fmt.Errorf("create api key for group %s: %w", groupKey, createErr)
		}
		createdIDs = append(createdIDs, created.ID)
		responses = append(responses, provisioningKeyResponse(*created, true))
	}
	return responses, createdIDs, nil
}

func normalizeProvisionAPIKeys(req ProvisionUserRequest) ([]ProvisioningAPIKeyRequest, error) {
	if !validProvisionExternalID(req.ExternalID) {
		return nil, fmt.Errorf("external_id may only contain letters, numbers, dot, underscore, colon, and hyphen")
	}
	items := make([]ProvisioningAPIKeyRequest, 0, len(req.APIKeys)+1)
	if req.APIKey != nil {
		items = append(items, *req.APIKey)
	}
	items = append(items, req.APIKeys...)
	if len(items) == 0 {
		return nil, fmt.Errorf("api_key or api_keys is required")
	}
	seen := map[string]struct{}{}
	for i := range items {
		items[i].Name = strings.TrimSpace(items[i].Name)
		if items[i].Name == "" {
			return nil, fmt.Errorf("api key name is required")
		}
		if items[i].GroupID != nil && *items[i].GroupID <= 0 {
			return nil, fmt.Errorf("api key group_id must be greater than zero")
		}
		key := provisionGroupKey(items[i].GroupID)
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate api key group_id: %s", key)
		}
		seen[key] = struct{}{}
	}
	return items, nil
}

func mergedProvisionGroups(groups []int64, keys []ProvisioningAPIKeyRequest) []int64 {
	result := append([]int64(nil), groups...)
	for _, item := range keys {
		if item.GroupID != nil {
			result = append(result, *item.GroupID)
		}
	}
	return mergeInt64Sets(nil, result)
}

func mergeInt64Sets(left, right []int64) []int64 {
	seen := map[int64]struct{}{}
	for _, values := range [][]int64{left, right} {
		for _, value := range values {
			if value > 0 {
				seen[value] = struct{}{}
			}
		}
	}
	result := make([]int64, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func validProvisionExternalID(value string) bool {
	for _, char := range strings.TrimSpace(value) {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '.', '_', ':', '-':
			continue
		default:
			return false
		}
	}
	return true
}

func equalInt64Sets(left, right []int64) bool {
	a, b := mergeInt64Sets(nil, left), mergeInt64Sets(nil, right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func notesWithExternalID(notes, externalID string) string {
	notes = strings.TrimSpace(notes)
	externalID = strings.TrimSpace(externalID)
	if externalID == "" || hasExternalID(notes, externalID) {
		return notes
	}
	marker := "[" + provisioningExternalIDPrefix + externalID + "]"
	if notes == "" {
		return marker
	}
	return notes + "\n" + marker
}

func hasExternalID(notes, externalID string) bool {
	return strings.Contains(notes, "["+provisioningExternalIDPrefix+strings.TrimSpace(externalID)+"]")
}

func provisionGroupKey(groupID *int64) string {
	if groupID == nil {
		return "unbound"
	}
	return fmt.Sprintf("%d", *groupID)
}

func provisioningKeyResponse(key service.APIKey, created bool) provisioningAPIKeyResponse {
	fingerprint := ""
	if key.Key != "" {
		sum := sha256.Sum256([]byte(key.Key))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	result := provisioningAPIKeyResponse{
		ID: key.ID, UserID: key.UserID, Name: key.Name, GroupID: key.GroupID,
		Status: key.Status, KeyFingerprint: fingerprint, Created: created,
	}
	if created {
		result.Key = key.Key
	}
	return result
}
