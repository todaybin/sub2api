//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyCodeBuddyUpstreamError(t *testing.T) {
	t.Parallel()
	require.Equal(t, codeBuddyErrHardCredit, classifyCodeBuddyUpstreamError(http.StatusPaymentRequired, []byte(`{}`)))
	require.Equal(t, codeBuddyErrHardCredit, classifyCodeBuddyUpstreamError(http.StatusOK, []byte(`{"code":"1","msg":"积分不足"}`)))
	require.Equal(t, codeBuddyErrSessionDead, classifyCodeBuddyUpstreamError(http.StatusUnauthorized, []byte(`{"code":"12153","msg":"Offline user session not found"}`)))
	require.Equal(t, codeBuddyErrToolChoice, classifyCodeBuddyUpstreamError(http.StatusBadRequest, []byte(`{"code":"11101","msg":"invalid tool_choice"}`)))
	require.Equal(t, codeBuddyErrIdentity, classifyCodeBuddyUpstreamError(http.StatusForbidden, []byte(`{"code":"12403","msg":"product token"}`)))
	require.Equal(t, codeBuddyErrContentFilter, classifyCodeBuddyUpstreamError(http.StatusOK, []byte(`{"msg":"内容审核拦截"}`)))
	require.Equal(t, codeBuddyErrSoftRate, classifyCodeBuddyUpstreamError(http.StatusTooManyRequests, []byte(`{}`)))
	require.Equal(t, codeBuddyErrServer, classifyCodeBuddyUpstreamError(http.StatusBadGateway, []byte(`{}`)))
}

func TestCodeBuddyClientErrorUsesRegionBrand(t *testing.T) {
	t.Parallel()
	domestic := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"region": CodeBuddyRegionDomestic}}
	_, errType, message, failover := codeBuddyClientError(domestic, http.StatusPaymentRequired, []byte(`{"msg":"积分不足"}`))
	require.Equal(t, "insufficient_quota", errType)
	require.Contains(t, message, "CodeBuddy")
	require.True(t, failover)

	intl := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"region": CodeBuddyRegionInternational}}
	_, _, message, _ = codeBuddyClientError(intl, http.StatusUnauthorized, []byte(`{"msg":"12153 Offline user session not found"}`))
	require.Contains(t, message, "WorkBuddy")
}

func TestCodeBuddyPackageRemain(t *testing.T) {
	t.Parallel()
	require.Equal(t, 40.0, codeBuddyPackageRemain(map[string]any{"CycleCapacitySize": 100, "CycleCapacityRemain": 40}))
	require.Equal(t, 20.0, codeBuddyPackageRemain(map[string]any{"CycleCapacitySize": 100, "CycleCapacityRemain": 40, "CycleCapacityUsed": 80}))
	require.Equal(t, 12.0, codeBuddyPackageRemain(map[string]any{"CycleCapacityRemain": 12, "CycleCapacityUsed": 3}))
	require.Equal(t, 7.0, codeBuddyPackageRemain(map[string]any{"CapacityRemain": 7}))
}
