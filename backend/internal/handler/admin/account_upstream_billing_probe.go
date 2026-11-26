package admin

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type upstreamBillingProbeEnabledRequest struct {
	Enabled *bool `json:"enabled" binding:"required"`
}

type upstreamBillingProbeBatchRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

type upstreamUsageQueryTestRequest struct {
	Platform    string            `json:"platform"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key"`
	AccessToken string            `json:"access_token"`
	UserID      string            `json:"user_id"`
	Variables   map[string]string `json:"variables,omitempty"`
	Config      json.RawMessage   `json:"config" binding:"required"`
}

func (h *AccountHandler) TestUpstreamUsageQuery(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	var req upstreamUsageQueryTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	var raw any
	if err := json.Unmarshal(req.Config, &raw); err != nil {
		response.BadRequest(c, "config must be valid JSON: "+err.Error())
		return
	}
	config, err := service.DecodeUpstreamUsageQueryConfig(raw)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result, err := h.upstreamBillingProbe.TestUsageQuery(c.Request.Context(), &service.UpstreamUsageQueryTestInput{
		Platform: req.Platform, BaseURL: req.BaseURL, APIKey: req.APIKey, AccessToken: req.AccessToken,
		UserID: req.UserID, Variables: req.Variables, Config: config,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) GetUpstreamBillingProbeSettings(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	settings, err := h.upstreamBillingProbe.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *AccountHandler) UpdateUpstreamBillingProbeSettings(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	var req service.UpstreamBillingProbeSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.upstreamBillingProbe.UpdateSettings(c.Request.Context(), &req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	settings, err := h.upstreamBillingProbe.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *AccountHandler) SetUpstreamBillingProbeEnabled(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req upstreamBillingProbeEnabledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.upstreamBillingProbe.SetAccountEnabled(c.Request.Context(), accountID, *req.Enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "enabled": *req.Enabled})
}

func (h *AccountHandler) ProbeUpstreamBilling(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	snapshot, err := h.upstreamBillingProbe.ProbeAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.UpstreamBillingProbeResult{AccountID: accountID, Snapshot: snapshot})
}

// PreviewUpstreamGroup reads the saved account's connection or the editor's draft credentials.
func (h *AccountHandler) PreviewUpstreamGroup(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	var req struct {
		AccountID int64  `json:"account_id" binding:"min=0"`
		Platform  string `json:"platform"`
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		ProxyID   *int64 `json:"proxy_id" binding:"omitempty,gt=0"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid group preview request")
		return
	}
	ctx := c.Request.Context()
	var account *service.Account
	if req.AccountID > 0 {
		// A stored key must only be sent with its stored URL, headers and proxy.
		if req.Platform != "" || req.BaseURL != "" || req.APIKey != "" || req.ProxyID != nil {
			response.BadRequest(c, "Use either a saved account or draft credentials")
			return
		}
		var err error
		account, err = h.adminService.GetAccount(ctx, req.AccountID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	} else {
		if !service.IsUpstreamBillingProbeIdentity(req.Platform, service.AccountTypeAPIKey) || strings.TrimSpace(req.BaseURL) == "" || strings.TrimSpace(req.APIKey) == "" {
			response.BadRequest(c, "A supported platform, base URL and API key are required")
			return
		}
		account = &service.Account{
			Platform: req.Platform, Type: service.AccountTypeAPIKey, ProxyID: req.ProxyID,
			Credentials: map[string]any{"base_url": strings.TrimSpace(req.BaseURL), "api_key": strings.TrimSpace(req.APIKey)},
		}
		if req.ProxyID != nil {
			proxy, err := h.adminService.GetProxy(ctx, *req.ProxyID)
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			account.Proxy = proxy
		}
	}
	group, err := h.upstreamBillingProbe.PreviewGroup(ctx, account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, group)
}

func (h *AccountHandler) ProbeUpstreamBillingBatch(c *gin.Context) {
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	var req upstreamBillingProbeBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.AccountIDs) == 0 || len(req.AccountIDs) > service.UpstreamBillingProbeMaxBatchSize {
		response.BadRequest(c, "account_ids must contain between 1 and 20 items")
		return
	}
	seen := make(map[int64]struct{}, len(req.AccountIDs))
	accountIDs := make([]int64, 0, len(req.AccountIDs))
	for _, accountID := range req.AccountIDs {
		if accountID <= 0 {
			response.BadRequest(c, "account_ids must contain positive IDs")
			return
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		accountIDs = append(accountIDs, accountID)
	}
	response.Success(c, gin.H{"results": h.upstreamBillingProbe.ProbeAccounts(c.Request.Context(), accountIDs)})
}
