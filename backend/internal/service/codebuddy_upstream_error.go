package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type codeBuddyUpstreamKind int

const (
	codeBuddyErrNone codeBuddyUpstreamKind = iota
	codeBuddyErrHardCredit
	codeBuddyErrSessionDead
	codeBuddyErrSoftRate
	codeBuddyErrToolChoice
	codeBuddyErrIdentity
	codeBuddyErrContentFilter
	codeBuddyErrNotFound
	codeBuddyErrServer
	codeBuddyErrClient
)

var codeBuddyHardCreditMarkers = []string{
	"insufficient credit", "no credit", "credit exhausted", "out of credit",
	"quota exceeded", "quota exhaust", "payment required", "credit not enough",
	"not enough credit",
	"积分不足", "额度不足", "余额不足", "积分用完", "额度用尽", "没有积分",
}

var codeBuddySessionDeadMarkers = []string{"Offline user session not found", "12153"}

var codeBuddyContentFilterMarkers = []string{
	"content-filter", "content_filter", "敏感内容", "内容审核", "无法响应您的请求", "审核",
}

func CodeBuddyBrandName(account *Account) string {
	if CodeBuddyRegionForAccount(account) == CodeBuddyRegionInternational {
		return "WorkBuddy"
	}
	return "CodeBuddy"
}

func codeBuddyEnvelopeCode(body []byte) string {
	if code := strings.TrimSpace(gjson.GetBytes(body, "code").String()); code != "" {
		return code
	}
	return strings.TrimSpace(gjson.GetBytes(body, "Code").String())
}

func codeBuddyEnvelopeMsg(body []byte) string {
	if msg := strings.TrimSpace(gjson.GetBytes(body, "msg").String()); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(gjson.GetBytes(body, "Msg").String()); msg != "" {
		return msg
	}
	return strings.TrimSpace(extractUpstreamErrorMessage(body))
}

func classifyCodeBuddyUpstreamError(status int, body []byte) codeBuddyUpstreamKind {
	msg := codeBuddyEnvelopeMsg(body)
	code := codeBuddyEnvelopeCode(body)
	combined := msg + " " + code + " " + string(body)
	lower := strings.ToLower(combined)

	if status == http.StatusPaymentRequired {
		return codeBuddyErrHardCredit
	}
	for _, marker := range codeBuddyHardCreditMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) || strings.Contains(combined, marker) {
			return codeBuddyErrHardCredit
		}
	}
	for _, marker := range codeBuddySessionDeadMarkers {
		if strings.Contains(combined, marker) {
			return codeBuddyErrSessionDead
		}
	}
	if code == "11101" || strings.Contains(lower, "tool_choice") && strings.Contains(lower, "11101") {
		return codeBuddyErrToolChoice
	}
	if code == "12403" {
		return codeBuddyErrIdentity
	}
	for _, marker := range codeBuddyContentFilterMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) || strings.Contains(combined, marker) {
			return codeBuddyErrContentFilter
		}
	}
	if status == http.StatusTooManyRequests {
		return codeBuddyErrSoftRate
	}
	if status == http.StatusNotFound {
		return codeBuddyErrNotFound
	}
	if status >= 500 {
		return codeBuddyErrServer
	}
	if status >= 400 || (code != "" && code != "0") {
		return codeBuddyErrClient
	}
	return codeBuddyErrNone
}

func codeBuddyClientError(account *Account, status int, body []byte) (httpStatus int, errType, message string, failover bool) {
	brand := CodeBuddyBrandName(account)
	kind := classifyCodeBuddyUpstreamError(status, body)
	msg := codeBuddyEnvelopeMsg(body)
	switch kind {
	case codeBuddyErrHardCredit:
		return http.StatusTooManyRequests, "insufficient_quota", brand + " 上游积分不足", true
	case codeBuddyErrSessionDead:
		return http.StatusUnauthorized, "authentication_error", brand + " 登录已失效，请重新授权", true
	case codeBuddyErrSoftRate:
		return http.StatusTooManyRequests, "rate_limit_error", brand + " 请求过于频繁，请稍后重试", true
	case codeBuddyErrToolChoice:
		return http.StatusBadRequest, "invalid_request_error", brand + " 不支持该 tool_choice 形态", false
	case codeBuddyErrIdentity:
		return http.StatusBadGateway, "api_error", brand + " 无法识别客户端身份", false
	case codeBuddyErrContentFilter:
		return http.StatusBadRequest, "invalid_request_error", "内容被 " + brand + " 审核拦截", false
	case codeBuddyErrNotFound:
		return http.StatusNotFound, "not_found_error", brand + " 上游暂时不可用", true
	case codeBuddyErrServer:
		return http.StatusBadGateway, "api_error", brand + " 上游暂时不可用", true
	default:
		if msg == "" {
			msg = "upstream error"
		}
		httpStatus = status
		if httpStatus < 400 {
			httpStatus = http.StatusBadGateway
		}
		return httpStatus, "api_error", brand + ": " + msg, false
	}
}

func codeBuddyErrorShouldFailover(status int, body []byte) bool {
	_, _, _, failover := codeBuddyClientError(nil, status, body)
	return failover
}

func (s *OpenAIGatewayService) applyCodeBuddyUpstreamPolicy(ctx context.Context, account *Account, status int, body []byte) bool {
	if s == nil || account == nil || account.Platform != PlatformCodeBuddy {
		return false
	}
	kind := classifyCodeBuddyUpstreamError(status, body)
	switch kind {
	case codeBuddyErrHardCredit:
		s.BlockAccountScheduling(account, time.Time{}, "codebuddy_credit")
		s.markCodeBuddyAutoDisabledByBalance(ctx, account)
		return true
	case codeBuddyErrSessionDead:
		s.BlockAccountScheduling(account, time.Time{}, "codebuddy_session_dead")
		return true
	case codeBuddyErrSoftRate:
		s.BlockAccountScheduling(account, time.Now().Add(60*time.Second), "429")
		return false
	case codeBuddyErrNotFound:
		s.BlockAccountScheduling(account, time.Now().Add(60*time.Second), "codebuddy_404")
		return false
	default:
		return false
	}
}

func codeBuddyPackageRemain(pkg map[string]any) float64 {
	if pkg == nil {
		return 0
	}
	size := codeBuddyNumber(pkg["CycleCapacitySize"])
	remain := codeBuddyNumber(pkg["CycleCapacityRemain"])
	used := codeBuddyNumber(pkg["CycleCapacityUsed"])
	if size > 0 && used > 0 {
		computed := size - used
		if computed < 0 {
			computed = 0
		}
		if remain > 0 && remain < computed {
			return remain
		}
		return computed
	}
	if _, ok := pkg["CycleCapacityRemain"]; ok {
		return remain
	}
	return codeBuddyNumber(pkg["CapacityRemain"])
}

func (s *OpenAIGatewayService) markCodeBuddyAutoDisabledByBalance(ctx context.Context, account *Account) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	settings := CodeBuddyCheckinSettingsForAccount(account)
	repo, ok := s.accountRepo.(BalanceSchedulingRepository)
	if !ok {
		return
	}
	_ = repo.UpdateCodeBuddyBalanceScheduling(ctx, account, settings, true, false)
}
