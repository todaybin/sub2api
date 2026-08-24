package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type codeBuddyOAuthStartRequest struct {
	AccountID    int64             `json:"account_id"`
	Region       string            `json:"region" binding:"required"`
	Name         string            `json:"name"`
	Notes        string            `json:"notes"`
	ProxyID      *int64            `json:"proxy_id"`
	GroupIDs     []int64           `json:"group_ids"`
	Concurrency  int               `json:"concurrency"`
	LoadFactor   *int              `json:"load_factor"`
	Priority     int               `json:"priority"`
	Rate         *float64          `json:"rate_multiplier"`
	ExpiresAt    *int64            `json:"expires_at"`
	AutoPause    *bool             `json:"auto_pause_on_expired"`
	ModelMapping map[string]string `json:"model_mapping"`
	ReferenceCostUnits float64 `json:"reference_cost_units"`
	ReferenceCredits float64 `json:"reference_credits"`
	TokensPerCredit float64 `json:"tokens_per_credit"`
}

// CodeBuddyOAuthStart initializes the upstream authorization state. The
// returned auth_url must be opened in a browser; auth/state is POST-only.
func (h *AccountHandler) CodeBuddyOAuthStart(c *gin.Context) {
	var req codeBuddyOAuthStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "region is required")
		return
	}
	if _, err := service.CodeBuddyEndpointForRegion(req.Region); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.codeBuddyOAuth.Start(c.Request.Context(), service.CodeBuddyOAuthStartInput{
		AccountID: req.AccountID, Region: req.Region, Name: req.Name, Notes: req.Notes, ProxyID: req.ProxyID, GroupIDs: req.GroupIDs,
		Concurrency: req.Concurrency, LoadFactor: req.LoadFactor, Priority: req.Priority, RateMultiplier: req.Rate,
		ExpiresAt: req.ExpiresAt, AutoPauseOnExpired: req.AutoPause, ModelMapping: req.ModelMapping,
		ReferenceCostUnits: req.ReferenceCostUnits, ReferenceCredits: req.ReferenceCredits, TokensPerCredit: req.TokensPerCredit,
	})
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, result)
}

type codeBuddyOAuthPollRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

func (h *AccountHandler) CodeBuddyOAuthPoll(c *gin.Context) {
	var req codeBuddyOAuthPollRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SessionID) == "" {
		response.BadRequest(c, "session_id is required")
		return
	}
	result, err := h.codeBuddyOAuth.Poll(c.Request.Context(), strings.TrimSpace(req.SessionID))
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	if result.Account != nil {
		response.Success(c, gin.H{"status": result.Status, "session_id": result.SessionID, "account": h.accountResponseFromService(result.Account)})
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) CodeBuddyAccountRefresh(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid account id")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updated, warning, err := h.refreshSingleAccount(c.Request.Context(), account)
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	if warning == "missing_project_id_temporary" {
		response.Success(c, gin.H{"message": "Token refreshed successfully, but project_id could not be retrieved (will retry automatically)", "warning": warning})
		return
	}
	response.Success(c, h.accountResponseFromService(updated))
}

// CodeBuddyCheckin runs the configured account check-in immediately.
func (h *AccountHandler) CodeBuddyCheckin(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	if h.codeBuddyCheckinRunner == nil {
		response.Error(c, http.StatusServiceUnavailable, "codebuddy checkin service is not configured")
		return
	}
	account, err := h.codeBuddyCheckinRunner.RunNow(c.Request.Context(), accountID)
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, h.accountResponseFromService(account))
}

func (h *AccountHandler) CodeBuddyModels(c *gin.Context) {
	if rawID := strings.TrimSpace(c.Query("account_id")); rawID != "" {
		accountID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || accountID <= 0 {
			response.BadRequest(c, "invalid account_id")
			return
		}
		account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		accountRegion := service.CodeBuddyRegionForAccount(account)
		if requestedRegion := strings.TrimSpace(c.Query("region")); requestedRegion != "" {
			if !strings.EqualFold(requestedRegion, service.CodeBuddyRegionDomestic) && !strings.EqualFold(requestedRegion, service.CodeBuddyRegionInternational) {
				response.BadRequest(c, "region must be domestic or international")
				return
			}
			if !strings.EqualFold(requestedRegion, accountRegion) {
				response.BadRequest(c, "requested CodeBuddy region does not match account region")
				return
			}
		}
		models, catalog, err := h.codeBuddyOAuth.ModelsWithCatalog(c.Request.Context(), account)
		if err != nil {
			response.Error(c, http.StatusBadGateway, err.Error())
			return
		}
		if len(catalog) > 0 {
			credentials := map[string]any{"region": service.CodeBuddyRegionForAccount(account), "models": models, "model_catalog": catalog}
			if mapping, ok := account.Credentials["model_mapping"]; ok {
				credentials["model_mapping"] = mapping
			}
			if uid := account.GetCredential("uid"); uid != "" {
				credentials["uid"] = uid
			}
			if enterpriseID := account.GetCredential("enterprise_id"); enterpriseID != "" {
				credentials["enterprise_id"] = enterpriseID
			}
			if _, updateErr := h.adminService.UpdateAccount(c.Request.Context(), accountID, &service.UpdateAccountInput{
				Credentials: credentials,
				Extra:       map[string]any{"codebuddy_region": service.CodeBuddyRegionForAccount(account), "codebuddy_models": models, "codebuddy_model_catalog": catalog},
			}); updateErr != nil {
				response.Error(c, http.StatusInternalServerError, "failed to persist CodeBuddy model catalog")
				return
			}
		}
		source := "stored"
		if strings.TrimSpace(account.GetCredential("access_token")) != "" {
			source = "upstream"
		}
		response.Success(c, gin.H{"models": models, "model_catalog": catalog, "default_model": "auto", "account_id": accountID, "region": service.CodeBuddyRegionForAccount(account), "endpoint": account.GetCredential("base_url"), "source": source})
		return
	}
	region := strings.TrimSpace(c.Query("region"))
	if region == "" {
		region = service.CodeBuddyRegionDomestic
	}
	if _, err := service.CodeBuddyEndpointForRegion(region); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	ids := service.CodeBuddyModelsForRegion(region)
	endpoint, _ := service.CodeBuddyEndpointForRegion(region)
	response.Success(c, gin.H{"models": ids, "model_catalog": service.CodeBuddyCatalogForIDs(ids), "default_model": "auto", "region": region, "endpoint": endpoint, "source": "fallback"})
}
