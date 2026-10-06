package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const keyBillingInfoSchemaVersion = 1

type keyBillingInfoResponse struct {
	Object                  string                   `json:"object"`
	SchemaVersion           int                      `json:"schema_version"`
	BillingScope            string                   `json:"billing_scope"`
	BillingMode             string                   `json:"billing_mode"`
	Balance                 *float64                 `json:"balance,omitempty"`
	SubscriptionID          *int64                   `json:"subscription_id,omitempty"`
	Usage                   *keyBillingUsageResponse `json:"usage,omitempty"`
	GroupRateMultiplier     float64                  `json:"group_rate_multiplier"`
	UserRateMultiplier      *float64                 `json:"user_rate_multiplier,omitempty"`
	ResolvedRateMultiplier  float64                  `json:"resolved_rate_multiplier"`
	PeakRateEnabled         bool                     `json:"peak_rate_enabled"`
	PeakStart               *string                  `json:"peak_start,omitempty"`
	PeakEnd                 *string                  `json:"peak_end,omitempty"`
	PeakRateMultiplier      *float64                 `json:"peak_rate_multiplier,omitempty"`
	AppliedPeakMultiplier   *float64                 `json:"applied_peak_multiplier,omitempty"`
	EffectiveRateMultiplier float64                  `json:"effective_rate_multiplier"`
	Timezone                *string                  `json:"timezone,omitempty"`
	ObservedAt              time.Time                `json:"observed_at"`
}

type keyBillingUsageResponse struct {
	Scope       string    `json:"scope"`
	Period      string    `json:"period"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Requests    int64     `json:"requests"`
	TotalTokens int64     `json:"total_tokens"`
}

// KeyBillingInfo returns the token billing multiplier effective for the authenticated API key.
// GET /v1/sub2api/billing
func (h *GatewayHandler) KeyBillingInfo(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if h.cfg != nil && h.cfg.RunMode == config.RunModeSimple {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Billing information is not supported in simple mode")
		return
	}
	if apiKey.RoutingMode == "smart" {
		h.smartKeyBillingInfo(c, apiKey)
		return
	}
	if apiKey.GroupID == nil {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "API key is not assigned to a group")
		return
	}
	if apiKey.Group == nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
		return
	}

	resolvedRate, ok := h.resolveKeyBillingRate(c, apiKey)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
		return
	}

	now := timezone.Now()
	result := buildKeyBillingInfo(apiKey, resolvedRate, now)
	if err := h.populateKeyBillingAccountInfo(c, apiKey, &result, now); err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (h *GatewayHandler) populateKeyBillingAccountInfo(c *gin.Context, apiKey *service.APIKey, result *keyBillingInfoResponse, now time.Time) error {
	if apiKey == nil || apiKey.Group == nil || result == nil {
		return nil
	}

	periodStart := now
	if apiKey.Group.IsSubscriptionType() {
		result.BillingMode = "subscription"
		subscription, ok := middleware2.GetSubscriptionFromContext(c)
		if !ok || subscription == nil {
			return nil
		}
		result.SubscriptionID = &subscription.ID
		periodStart = subscription.StartsAt
	} else {
		result.BillingMode = "balance"
		if apiKey.User != nil {
			balance := apiKey.User.Balance
			result.Balance = &balance
		}
		localNow := now.In(timezone.Location())
		periodStart = time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, timezone.Location())
	}

	if h.usageService == nil || apiKey.ID <= 0 {
		return nil
	}
	if periodStart.After(now) {
		periodStart = now
	}
	stats, err := h.usageService.GetStatsByAPIKey(c.Request.Context(), apiKey.ID, periodStart, now)
	if err != nil {
		return err
	}
	result.Usage = &keyBillingUsageResponse{
		Scope:       "api_key",
		Period:      "current_billing_period",
		PeriodStart: periodStart.UTC(),
		PeriodEnd:   now.UTC(),
		Requests:    stats.TotalRequests,
		TotalTokens: stats.TotalTokens,
	}
	return nil
}

func (h *GatewayHandler) resolveKeyBillingRate(c *gin.Context, apiKey *service.APIKey) (float64, bool) {
	groupRate := apiKey.Group.RateMultiplier
	switch apiKey.Group.Platform {
	case service.PlatformOpenAI, service.PlatformGrok:
		if h.openAIGatewayService == nil {
			return 0, false
		}
		return h.openAIGatewayService.ResolveUserGroupRateMultiplier(c.Request.Context(), apiKey.UserID, *apiKey.GroupID, groupRate), true
	default:
		if h.gatewayService == nil {
			return 0, false
		}
		return h.gatewayService.ResolveUserGroupRateMultiplier(c.Request.Context(), apiKey.UserID, *apiKey.GroupID, groupRate), true
	}
}

func buildKeyBillingInfo(apiKey *service.APIKey, resolvedRate float64, now time.Time) keyBillingInfoResponse {
	groupRate := apiKey.Group.RateMultiplier
	var userRate *float64
	if resolvedRate != groupRate {
		userRate = &resolvedRate
	}
	appliedPeak := apiKey.Group.PeakMultiplierAt(now)

	response := keyBillingInfoResponse{
		Object:                  "sub2api.key_billing",
		SchemaVersion:           keyBillingInfoSchemaVersion,
		BillingScope:            "token",
		BillingMode:             "balance",
		GroupRateMultiplier:     groupRate,
		UserRateMultiplier:      userRate,
		ResolvedRateMultiplier:  resolvedRate,
		PeakRateEnabled:         apiKey.Group.PeakRateEnabled,
		EffectiveRateMultiplier: resolvedRate * appliedPeak,
		ObservedAt:              now.UTC(),
	}
	if apiKey.Group.PeakRateEnabled {
		response.PeakStart = &apiKey.Group.PeakStart
		response.PeakEnd = &apiKey.Group.PeakEnd
		response.PeakRateMultiplier = &apiKey.Group.PeakRateMultiplier
		response.AppliedPeakMultiplier = &appliedPeak
		tz := timezone.Location().String()
		response.Timezone = &tz
	}
	return response
}

// Smart keys have no single effective multiplier. Each selected group settles independently.
func (h *GatewayHandler) smartKeyBillingInfo(c *gin.Context, key *service.APIKey) {
	now := timezone.Now()
	groups := make([]map[string]any, 0, len(key.SmartGroups))
	for _, group := range key.SmartGroups {
		local := *key
		local.Group, local.GroupID = group, &group.ID
		rate, ok := h.resolveKeyBillingRate(c, &local)
		if !ok {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
			return
		}
		info := buildKeyBillingInfo(&local, rate, now)
		mode := "balance"
		available := key.User != nil && !middleware2.APIKeyBalanceBelowAuthThreshold(key.User.Balance, h.cfg)
		var subID *int64
		if group.IsSubscriptionType() {
			mode = "subscription"
			if h.smartSubscriptions == nil {
				h.errorResponse(c, http.StatusInternalServerError, "api_error", "Subscription information is unavailable")
				return
			}
			sub, err := h.smartSubscriptions.GetActiveSubscription(c.Request.Context(), key.UserID, group.ID)
			if errors.Is(err, service.ErrSubscriptionNotFound) || errors.Is(err, service.ErrSubscriptionExpired) || errors.Is(err, service.ErrSubscriptionSuspended) {
				available = false
			} else if err != nil {
				h.errorResponse(c, http.StatusInternalServerError, "api_error", "Subscription information is unavailable")
				return
			}
			if sub != nil {
				subID = &sub.ID
				maintenance, limitErr := h.smartSubscriptions.ValidateAndCheckLimits(sub, group)
				if maintenance {
					sub, err = h.smartSubscriptions.EnsureWindowMaintenance(c.Request.Context(), sub)
					if err != nil {
						h.errorResponse(c, http.StatusInternalServerError, "api_error", "Subscription information is unavailable")
						return
					}
					_, limitErr = h.smartSubscriptions.ValidateAndCheckLimits(sub, group)
				}
				available = limitErr == nil
			}
		}
		info.BillingMode, info.SubscriptionID = mode, subID
		pricing := gin.H{
			"token": info, "model_pricing": group.ModelPricing,
			"long_context_pricing_enabled": group.LongContextPricingEnabled,
			"image":                        gin.H{"rate_independent": group.ImageRateIndependent, "rate_multiplier": rate, "price_1k": group.ImagePrice1K, "price_2k": group.ImagePrice2K, "price_4k": group.ImagePrice4K},
			"video":                        gin.H{"rate_independent": group.VideoRateIndependent, "rate_multiplier": rate, "price_480p": group.VideoPrice480P, "price_720p": group.VideoPrice720P, "price_1080p": group.VideoPrice1080P, "model_prices": group.VideoModelPrices},
			"audio":                        gin.H{"realtime_price_per_min": group.AudioRealtimePricePerMin, "tts_price_per_million_chars": group.AudioTTSPricePerMillionChars, "stt_price_per_hour": group.AudioSTTPricePerHour},
			"web_search_price_per_call":    group.WebSearchPricePerCall, "search_price_per_1k": group.SearchPricePer1k,
		}
		if group.ImageRateIndependent {
			pricing["image"].(gin.H)["rate_multiplier"] = group.ImageRateMultiplier
		}
		if group.VideoRateIndependent {
			pricing["video"].(gin.H)["rate_multiplier"] = group.VideoRateMultiplier
		}
		groups = append(groups, map[string]any{"group_id": group.ID, "group_name": group.Name, "billing_mode": mode, "available": available, "subscription_id": subID, "pricing": pricing})
	}
	result := gin.H{"object": "sub2api.key_billing", "schema_version": 2, "billing_scope": "selected_group", "routing_mode": "smart", "routing_strategy": key.RoutingStrategy, "groups": groups, "observed_at": now.UTC()}
	if key.User != nil {
		result["balance"] = key.User.Balance
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}
