package admin

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ProvisioningHandler creates a regular user and their first API key in one
// request. It is available through both the normal admin API and the signed
// integration gateway.
type ProvisioningHandler struct {
	adminService        service.AdminService
	apiKeyService       *service.APIKeyService
	subscriptionService *service.SubscriptionService
}

func NewProvisioningHandler(adminService service.AdminService, apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService) *ProvisioningHandler {
	return &ProvisioningHandler{adminService: adminService, apiKeyService: apiKeyService, subscriptionService: subscriptionService}
}

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

type ProvisioningSubscriptionRequest struct {
	GroupID      int64  `json:"group_id" binding:"required,gt=0"`
	ValidityDays int    `json:"validity_days" binding:"omitempty,min=1,max=36500"`
	Notes        string `json:"notes"`
}

type ProvisionUserRequest struct {
	Email         string                           `json:"email" binding:"required,email"`
	Password      string                           `json:"password" binding:"required,min=6"`
	Username      string                           `json:"username"`
	Notes         string                           `json:"notes"`
	Concurrency   int                              `json:"concurrency" binding:"gte=0"`
	RPMLimit      int                              `json:"rpm_limit" binding:"gte=0"`
	AllowedGroups []int64                          `json:"allowed_groups"`
	Balance       *float64                         `json:"balance" binding:"omitempty,gte=0"`
	APIKey        ProvisioningAPIKeyRequest        `json:"api_key" binding:"required"`
	Subscription  *ProvisioningSubscriptionRequest `json:"subscription"`
}

// Create creates a regular user, an optional subscription, then the API key.
// The surrounding compensation keeps failed provisioning from leaving a usable
// account when an later operation cannot be completed.
func (h *ProvisioningHandler) Create(c *gin.Context) {
	var req ProvisionUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if h.adminService == nil || h.apiKeyService == nil || h.subscriptionService == nil {
		response.Error(c, 500, "Provisioning service unavailable")
		return
	}

	executeAdminIdempotentJSON(c, "admin.users.provision", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		user, err := h.adminService.CreateUser(ctx, &service.CreateUserInput{
			Email: req.Email, Password: req.Password, Username: req.Username, Notes: req.Notes,
			Role: service.RoleUser, Balance: req.Balance, Concurrency: req.Concurrency,
			RPMLimit: req.RPMLimit, AllowedGroups: req.AllowedGroups, ActorAdminID: getAdminIDFromContext(c),
		})
		if err != nil {
			return nil, err
		}
		committed := false
		defer func() {
			if !committed {
				_ = h.adminService.DeleteUser(context.Background(), user.ID)
			}
		}()

		var subscription *service.UserSubscription
		if req.Subscription != nil {
			subscription, err = h.subscriptionService.AssignSubscription(ctx, &service.AssignSubscriptionInput{
				UserID: user.ID, GroupID: req.Subscription.GroupID, ValidityDays: req.Subscription.ValidityDays,
				AssignedBy: getAdminIDFromContext(c), Notes: req.Subscription.Notes,
			})
			if err != nil {
				return nil, fmt.Errorf("assign subscription: %w", err)
			}
		}
		apiKey, err := h.apiKeyService.Create(ctx, user.ID, service.CreateAPIKeyRequest{
			Name: req.APIKey.Name, GroupID: req.APIKey.GroupID, CustomKey: req.APIKey.CustomKey,
			IPWhitelist: req.APIKey.IPWhitelist, IPBlacklist: req.APIKey.IPBlacklist, Quota: req.APIKey.Quota,
			ExpiresInDays: req.APIKey.ExpiresInDays, RateLimit5h: req.APIKey.RateLimit5h,
			RateLimit1d: req.APIKey.RateLimit1d, RateLimit7d: req.APIKey.RateLimit7d,
		})
		if err != nil {
			return nil, fmt.Errorf("create api key: %w", err)
		}
		committed = true
		return gin.H{
			"user":         dto.UserFromServiceAdmin(user),
			"api_key":      dto.APIKeyFromService(apiKey),
			"subscription": dto.UserSubscriptionFromServiceAdmin(subscription),
		}, nil
	})
}
