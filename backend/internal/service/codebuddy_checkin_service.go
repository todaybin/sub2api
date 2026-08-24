package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	CodeBuddyCheckinExtraKey              = "codebuddy_checkin"
	CodeBuddyCheckinDefaultCron           = "0 9 * * *"
	codeBuddyBillingDomesticEndpoint      = "https://www.codebuddy.cn"
	codeBuddyBillingInternationalEndpoint = "https://www.workbuddy.ai"
)

// CodeBuddyCheckinSettings is persisted below accounts.extra. The nested
// object keeps scheduler state separate from credentials and billing probes.
type CodeBuddyCheckinSettings struct {
	Enabled               bool    `json:"enabled"`
	Cron                  string  `json:"cron"`
	NextRunAt             string  `json:"next_run_at,omitempty"`
	LastRunAt             string  `json:"last_run_at,omitempty"`
	LastStatus            string  `json:"last_status,omitempty"`
	LastAction            string  `json:"last_action,omitempty"`
	LastMessage           string  `json:"last_message,omitempty"`
	LastCreditGranted     float64 `json:"last_credit_granted"`
	LastCreditRemaining   float64 `json:"last_credit_remaining"`
	LastError             string  `json:"last_error,omitempty"`
	TrialClaimed          bool    `json:"trial_claimed,omitempty"`
	AuthInvalid           bool    `json:"auth_invalid,omitempty"`
	AutoDisabledByBalance bool    `json:"auto_disabled_by_balance,omitempty"`
}

type CodeBuddyCheckinResult struct {
	Status          string  `json:"status"`
	Action          string  `json:"action"`
	Message         string  `json:"message,omitempty"`
	CreditGranted   float64 `json:"credit_granted,omitempty"`
	CreditRemaining float64 `json:"credit_remaining,omitempty"`
	TrialClaimed    bool    `json:"trial_claimed,omitempty"`
	AuthInvalid     bool    `json:"auth_invalid,omitempty"`
}

func CodeBuddyCheckinSettingsForAccount(account *Account) CodeBuddyCheckinSettings {
	settings := CodeBuddyCheckinSettings{Enabled: true, Cron: CodeBuddyCheckinDefaultCron}
	if account == nil || account.Extra == nil {
		return settings
	}
	raw, ok := account.Extra[CodeBuddyCheckinExtraKey]
	if !ok {
		return settings
	}
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &settings) != nil {
		return CodeBuddyCheckinSettings{Enabled: true, Cron: CodeBuddyCheckinDefaultCron}
	}
	if strings.TrimSpace(settings.Cron) == "" {
		settings.Cron = CodeBuddyCheckinDefaultCron
	}
	return settings
}

func codeBuddyCheckinHeaders(account *Account) map[string]string {
	headers := map[string]string{
		"Accept":        "application/json",
		"User-Agent":    "CLI/2.63.2 CodeBuddy/2.63.2",
		"Authorization": "Bearer " + account.GetCredential("access_token"),
	}
	if uid := account.GetCredential("uid"); uid != "" {
		headers["X-User-Id"] = uid
	}
	if enterprise := account.GetCredential("enterprise_id"); enterprise != "" {
		headers["X-Enterprise-Id"] = enterprise
		headers["X-Tenant-Id"] = enterprise
	}
	if domain := account.GetCredential("domain"); domain != "" {
		headers["X-Domain"] = domain
	}
	return headers
}

func codeBuddyCheckinBillingBase(account *Account) string {
	if CodeBuddyRegionForAccount(account) == CodeBuddyRegionInternational {
		return codeBuddyBillingInternationalEndpoint
	}
	return codeBuddyBillingDomesticEndpoint
}

func (s *CodeBuddyOAuthService) billingPost(ctx context.Context, account *Account, path string, body any) (map[string]any, int, error) {
	if account == nil {
		return nil, 0, errors.New("codebuddy account required")
	}
	b, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return nil, 0, marshalErr
	}
	sess := &codeBuddySession{Endpoint: codeBuddyCheckinBillingBase(account), Region: CodeBuddyRegionForAccount(account), Domain: account.GetCredential("domain"), ProxyID: account.ProxyID}
	var raw []byte
	var status int
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		raw, status, err = s.do(ctx, sess, http.MethodPost, path, b, codeBuddyCheckinHeaders(account))
		if err == nil && status < 500 {
			break
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, status, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
			}
		}
	}
	if err != nil {
		return nil, status, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, status, err
	}
	code := cbCheckinString(envelope["code"])
	if code != "" && code != "0" {
		return nil, status, fmt.Errorf("codebuddy %s: code=%s msg=%s", path, code, cbCheckinString(envelope["msg"]))
	}
	if code == "" {
		code = cbCheckinString(envelope["Code"])
		if code != "" && code != "0" {
			return nil, status, fmt.Errorf("codebuddy %s: code=%s msg=%s", path, code, cbCheckinString(envelope["Msg"]))
		}
	}
	data, _ := envelope["data"].(map[string]any)
	if data == nil {
		data, _ = envelope["Data"].(map[string]any)
	}
	if data == nil {
		data = envelope
	}
	return data, status, nil
}

func cbCheckinString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func codeBuddyNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	default:
		return 0
	}
}

func (s *CodeBuddyOAuthService) checkinStatus(ctx context.Context, account *Account) (map[string]any, error) {
	for _, endpoint := range []string{"/v2/billing/meter/checkin-activity-status", "/v2/billing/meter/checkin-status"} {
		data, status, err := s.billingPost(ctx, account, endpoint, map[string]any{})
		if err == nil && status < 400 {
			return data, nil
		}
		if status == http.StatusUnauthorized {
			return nil, err
		}
	}
	return nil, errors.New("codebuddy checkin status unavailable")
}

func (s *CodeBuddyOAuthService) Checkin(ctx context.Context, account *Account) (*CodeBuddyCheckinResult, *Account, error) {
	result, updated, err := s.checkinOnce(ctx, account)
	if err == nil || updated == nil || strings.TrimSpace(updated.GetCredential("refresh_token")) == "" {
		return result, updated, err
	}
	// A stale access token is common for long-lived accounts. Refresh once and
	// retry the upstream operation; a second failure is returned unchanged.
	refreshed, refreshErr := s.Refresh(ctx, updated)
	if refreshErr != nil {
		return result, updated, err
	}
	return s.checkinOnce(ctx, refreshed)
}

func (s *CodeBuddyOAuthService) checkinOnce(ctx context.Context, account *Account) (*CodeBuddyCheckinResult, *Account, error) {
	if account == nil || account.Platform != PlatformCodeBuddy || account.Type != AccountTypeOAuth {
		return nil, account, errors.New("codebuddy oauth account required")
	}
	if strings.TrimSpace(account.GetCredential("access_token")) == "" && strings.TrimSpace(account.GetCredential("refresh_token")) != "" {
		updated, err := s.Refresh(ctx, account)
		if err != nil {
			return nil, account, err
		}
		account = updated
	}
	if strings.TrimSpace(account.GetCredential("access_token")) == "" {
		return nil, account, errors.New("codebuddy access token missing")
	}
	result := &CodeBuddyCheckinResult{}
	if CodeBuddyRegionForAccount(account) == CodeBuddyRegionInternational {
		data, _, err := s.billingPost(ctx, account, "/billing/ide/trial", map[string]any{})
		if err != nil {
			if strings.Contains(err.Error(), "14051") || strings.Contains(strings.ToLower(err.Error()), "already") {
				result.Status, result.Action, result.Message, result.TrialClaimed = "already", "trial", "trial already claimed", true
				return result, account, nil
			}
			return nil, account, err
		}
		result.Status, result.Action, result.Message, result.TrialClaimed = "success", "trial", "trial claimed", true
		result.CreditGranted = codeBuddyNumber(data["credit"])
		return result, account, nil
	}
	statusData, err := s.checkinStatus(ctx, account)
	if err != nil {
		return nil, account, err
	}
	active := statusData["active"] == true || codeBuddyNumber(statusData["active"]) > 0
	today := statusData["today_checked_in"] == true || statusData["todayCheckedIn"] == true
	if today {
		result.Status, result.Action, result.Message = "already", "daily_checkin", "today already checked in"
		return result, account, nil
	}
	if !active {
		result.Status, result.Action, result.Message = "inactive", "daily_checkin", "checkin activity inactive"
		return result, account, nil
	}
	data, _, err := s.billingPost(ctx, account, "/v2/billing/meter/daily-checkin", map[string]any{})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already") || strings.Contains(err.Error(), "已签") {
			result.Status, result.Action, result.Message = "already", "daily_checkin", "today already checked in"
			return result, account, nil
		}
		return nil, account, err
	}
	result.Status, result.Action, result.Message = "success", "daily_checkin", "checkin successful"
	result.CreditGranted = codeBuddyNumber(data["credit"])
	return result, account, nil
}

// CreditRemaining queries the same resource endpoint used by the billing probe.
func (s *CodeBuddyOAuthService) CreditRemaining(ctx context.Context, account *Account) (float64, bool, error) {
	now := time.Now().Format("2006-01-02 15:04:05")
	body := map[string]any{"PageNumber": 1, "PageSize": 100, "ProductCode": "p_tcaca", "Status": []int{0, 3}, "PackageEndTimeRangeBegin": now, "PackageEndTimeRangeEnd": time.Now().AddDate(101, 0, 0).Format("2006-01-02 15:04:05")}
	data, _, err := s.billingPost(ctx, account, "/v2/billing/meter/get-user-resource", body)
	if err != nil {
		return 0, false, err
	}
	resp, _ := data["Response"].(map[string]any)
	if resp == nil {
		resp = data
	}
	inner, _ := resp["Data"].(map[string]any)
	if inner == nil {
		inner = resp
	}
	accounts, _ := inner["Accounts"].([]any)
	var remain float64
	for _, raw := range accounts {
		pkg, _ := raw.(map[string]any)
		if pkg == nil {
			continue
		}
		cycleSize := codeBuddyNumber(pkg["CycleCapacitySize"])
		if cycleSize > 0 {
			r := codeBuddyNumber(pkg["CycleCapacityRemain"])
			if r < 0 {
				r = 0
			}
			if r > cycleSize {
				r = cycleSize
			}
			remain += r
			continue
		}
		remain += codeBuddyNumber(pkg["CycleCapacityRemain"])
		if codeBuddyNumber(pkg["CycleCapacityRemain"]) == 0 {
			remain += codeBuddyNumber(pkg["CapacityRemain"])
		}
	}
	return remain, len(accounts) > 0, nil
}
