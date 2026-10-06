package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const smartWSResolverKey = "smart_route_ws_resolver"

// SmartRouting runs before group policies and platform dispatch. Models and
// usage discovery retain the full authorized catalog without selecting a group.
func (h *GatewayHandler) SmartRouting(subscriptions *service.SubscriptionService) gin.HandlerFunc {
	h.smartSubscriptions = subscriptions
	return func(c *gin.Context) {
		key, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || key == nil || key.RoutingMode != "smart" {
			c.Next()
			return
		}
		var release func()
		defer func() {
			if release != nil {
				release()
			}
		}()
		resolve := func(body []byte, ws bool) error {
			var err error
			release, err = h.resolveSmartRequest(c, body, ws, subscriptions)
			return err
		}
		path := c.Request.URL.Path
		if c.Param("request_id") != "" || c.Param("call_id") != "" {
			if err := h.resolveSmartOwner(c, key, subscriptions); err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, errSmartOwnerNotFound) {
					status = http.StatusNotFound
				}
				middleware.AnthropicErrorWriter(c, status, err.Error())
				c.Abort()
				return
			}
			c.Next()
			return
		}
		if c.Request.Method == http.MethodGet && strings.HasSuffix(path, "/responses") {
			c.Set(smartWSResolverKey, resolve)
			c.Next()
			return
		}
		if c.Request.Method == http.MethodGet && (strings.Contains(path, "/models") || strings.HasSuffix(path, "/usage") || strings.HasSuffix(path, "/billing") || strings.Contains(path, "/images/tasks/") || strings.Contains(path, "/images/batches")) {
			c.Next()
			return
		}
		if (c.Request.Method == http.MethodDelete || strings.HasSuffix(path, "/cancel")) && strings.Contains(path, "/images/batches/") {
			c.Next()
			return
		}
		var body []byte
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodDelete {
			var err error
			body, err = httputil.ReadRequestBodyWithPrealloc(c.Request)
			if err != nil {
				status := http.StatusBadRequest
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					status = http.StatusRequestEntityTooLarge
				}
				middleware.AnthropicErrorWriter(c, status, "Failed to read request body")
				c.Abort()
				return
			}
			requestmodel.ResetRequestBody(c.Request, body)
		}
		if err := resolve(body, false); err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, errSmartModelRequired) {
				status = http.StatusBadRequest
			}
			if strings.Contains(path, "/v1beta/") {
				middleware.GoogleErrorWriter(c, status, err.Error())
			} else {
				middleware.AnthropicErrorWriter(c, status, err.Error())
			}
			c.Abort()
			return
		}
		c.Next()
	}
}

var errSmartModelRequired = errors.New("smart routing requires a model or a known session owner")

func smartEndpoint(c *gin.Context) string {
	path := c.Request.URL.Path
	switch {
	case strings.Contains(path, "/images/batches"):
		return "batch_image"
	case strings.Contains(path, "/v1beta/"):
		return service.CompositeRouteEndpointGemini
	case strings.Contains(path, "/count_tokens"):
		return service.CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return service.CompositeRouteEndpointMessages
	case strings.Contains(path, "/chat/completions"):
		return service.CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return service.CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return service.CompositeRouteEndpointImages
	case strings.Contains(path, "/videos"):
		return "video"
	case strings.Contains(path, "/audio") || strings.Contains(path, "/realtime") || strings.Contains(path, "/tts") || strings.Contains(path, "/stt") || strings.Contains(path, "/custom-voices"):
		return "audio"
	case strings.Contains(path, "/live"):
		return "live"
	case strings.Contains(path, "/alpha/search"):
		return "alpha_search"
	case strings.Contains(path, "/systemone"):
		return "systemone"
	default:
		return service.CompositeRouteEndpointResponses
	}
}

func (h *GatewayHandler) resolveSmartRequest(c *gin.Context, body []byte, ws bool, subscriptions *service.SubscriptionService) (func(), error) {
	key, _ := middleware.GetAPIKeyFromContext(c)
	if key == nil || key.RoutingMode != "smart" {
		return nil, nil
	}
	SetClaudeCodeClientContext(c, body, nil)
	models := requestmodel.FromBodyCandidates(c.FullPath(), c.ContentType(), body)
	if ws {
		models = requestmodel.FromBodyCandidates("", "application/json", body)
	}
	if model := strings.TrimSpace(c.Param("modelAction")); model != "" {
		models = []string{strings.SplitN(strings.TrimPrefix(model, "models/"), ":", 2)[0]}
	}
	if len(models) == 0 {
		if model := strings.TrimSpace(c.Query("model")); model != "" {
			models = []string{model}
		}
	}
	if len(models) == 0 && smartEndpoint(c) == "audio" {
		models = []string{"grok-4.5"}
	}
	if len(models) == 0 {
		return nil, errSmartModelRequired
	}
	copyKey := *key
	copyKey.SmartGroups = make([]*service.Group, 0, len(key.SmartGroups))
	for _, group := range key.SmartGroups {
		allowed := true
		for _, model := range models {
			if group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(model) {
				allowed = false
				break
			}
		}
		if allowed {
			copyKey.SmartGroups = append(copyKey.SmartGroups, group)
		}
	}
	// Use the existing client/content session identity consistently for both
	// group affinity and the account scheduler, independent of the chosen platform.
	session := ""
	if h.openAIGatewayService != nil {
		session = h.openAIGatewayService.GenerateSessionHash(c, body)
	}
	if parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), "anthropic"); err == nil && parsed.MetadataUserID != "" {
		if hash := h.gatewayService.GenerateSessionHash(parsed); hash != "" {
			session = hash
		}
	}
	if ws && session == "" {
		session = "smart-ws:" + uuid.NewString()
	}
	ctx := context.WithValue(c.Request.Context(), ctxkey.SmartSessionHash, session)
	request := service.SmartRouteRequest{Model: models[0], Endpoint: smartEndpoint(c), Body: body, SessionHash: session, WebSocket: ws, Path: c.Request.URL.Path, ContentType: c.ContentType()}
	request.Platform, _ = middleware.GetForcePlatformFromContext(c)
	selectedSubs := map[int64]*service.UserSubscription{}
	group, selection, err := h.gatewayService.SelectSmartRoute(ctx, &copyKey, request, h.openAIGatewayService, func(group *service.Group) (bool, error) {
		allowed, sub, err := h.admitSmartGroup(c, key, group, subscriptions)
		if sub != nil {
			selectedSubs[group.ID] = sub
		}
		return allowed, err
	})
	if err != nil {
		return nil, err
	}
	copyKey.Group, copyKey.GroupID = group, &group.ID
	c.Set(string(middleware.ContextKeyAPIKey), &copyKey)
	c.Set(string(middleware.ContextKeySubscription), selectedSubs[group.ID])
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx = context.WithValue(ctx, ctxkey.SelectedGroupID, group.ID)
	ctx = context.WithValue(ctx, ctxkey.SmartSelection, selection)
	c.Request = c.Request.WithContext(ctx)
	return selection.ReleaseRequest, nil
}

// Task/session lookups resolve their original identity-bound group without
// running any fresh strategy or migrating to a different upstream session.
func (h *GatewayHandler) resolveSmartOwner(c *gin.Context, key *service.APIKey, subscriptions *service.SubscriptionService) error {
	if h.openAIGatewayService == nil {
		return errors.New("session owner service unavailable")
	}
	for _, group := range key.SmartGroups {
		found := false
		if callID := c.Param("call_id"); callID != "" {
			_, err := h.openAIGatewayService.GetLiveCallForIdentity(c.Request.Context(), callID, service.LiveCallIdentity{APIKeyID: key.ID, UserID: key.UserID, GroupID: &group.ID})
			if errors.Is(err, service.ErrLiveIdentityMismatch) || errors.Is(err, service.ErrLiveCallNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			found = true
		} else {
			id, err := h.openAIGatewayService.ResolveGrokMediaVideoRequestAccount(c.Request.Context(), &group.ID, c.Param("request_id"), key.UserID, key.ID)
			if errors.Is(err, service.ErrStickySessionNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			found = id > 0
		}
		if found {
			allowed, sub, err := h.admitSmartGroup(c, key, group, subscriptions)
			if err != nil {
				return err
			}
			if !allowed {
				return errors.New("session owner group billing eligibility denied")
			}
			c.Set(string(middleware.ContextKeySubscription), sub)
			copyKey := *key
			copyKey.Group, copyKey.GroupID = group, &group.ID
			c.Set(string(middleware.ContextKeyAPIKey), &copyKey)
			ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
			ctx = context.WithValue(ctx, ctxkey.SelectedGroupID, group.ID)
			c.Request = c.Request.WithContext(ctx)
			return nil
		}
	}
	return errSmartOwnerNotFound
}

var errSmartOwnerNotFound = errors.New("session or task is not available for this key")

func (h *GatewayHandler) admitSmartGroup(c *gin.Context, key *service.APIKey, group *service.Group, subscriptions *service.SubscriptionService) (bool, *service.UserSubscription, error) {
	if h.cfg != nil && h.cfg.RunMode == config.RunModeSimple {
		return true, nil, nil
	}
	if key.User == nil {
		return false, nil, errors.New("key user unavailable")
	}
	if !group.IsSubscriptionType() {
		return !middleware.APIKeyBalanceBelowAuthThreshold(key.User.Balance, h.cfg), nil, nil
	}
	if subscriptions == nil {
		return false, nil, errors.New("subscription service unavailable")
	}
	ctx := c.Request.Context()
	sub, err := subscriptions.GetActiveSubscription(ctx, key.UserID, group.ID)
	if errors.Is(err, service.ErrSubscriptionNotFound) || errors.Is(err, service.ErrSubscriptionExpired) || errors.Is(err, service.ErrSubscriptionSuspended) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	maintenance, validErr := subscriptions.ValidateAndCheckLimits(sub, group)
	if maintenance {
		sub, err = subscriptions.EnsureWindowMaintenance(ctx, sub)
		if err != nil {
			return false, nil, err
		}
		_, validErr = subscriptions.ValidateAndCheckLimits(sub, group)
	}
	if validErr != nil {
		return false, nil, nil
	}
	return true, sub, nil
}
