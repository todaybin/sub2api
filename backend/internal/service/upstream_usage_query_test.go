package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamUsageQueryTemplatesMapGenericAndNewAPI(t *testing.T) {
	generic, err := DecodeUpstreamUsageQueryConfig(GenericUpstreamUsageQueryTemplate())
	require.NoError(t, err)
	result, err := mapUsageQueryResponse([]byte(`{"balance":"56.5","currency":"CNY"}`), generic)
	require.NoError(t, err)
	require.True(t, result.IsValid)
	require.Equal(t, 56.5, *result.Remaining)
	require.Equal(t, "CNY", result.Unit)

	newAPI, err := DecodeUpstreamUsageQueryConfig(NewAPIUpstreamUsageQueryTemplate())
	require.NoError(t, err)
	result, err = mapUsageQueryResponse([]byte(`{"success":true,"data":{"group":"vip","quota":25000000,"used_quota":500000}}`), newAPI)
	require.NoError(t, err)
	require.Equal(t, "vip", result.PlanName)
	require.Equal(t, 50.0, *result.Remaining)
	require.Equal(t, 1.0, *result.Used)
	require.Equal(t, 51.0, *result.Total)
	require.Equal(t, "USD", result.Unit)
}

func TestUpstreamUsageQueryRejectsUnknownFieldsAndUnsupportedExpressions(t *testing.T) {
	_, err := DecodeUpstreamUsageQueryConfig(map[string]any{
		"version": 1, "template_type": "custom", "result_type": "balance", "unknown": true,
	})
	require.Error(t, err)

	config := GenericUpstreamUsageQueryTemplate()
	config.Mapping.Remaining.Derive = "javascript"
	require.Error(t, ValidateUpstreamUsageQueryConfig(config))
}

func TestProtectUpstreamUsageQuerySecrets(t *testing.T) {
	extra := map[string]any{
		UpstreamBillingUsageQueryConfigExtraKey: map[string]any{
			"version": 1, "template_type": "custom", "result_type": "balance",
			"request":   map[string]any{"url": "{{baseUrl}}/balance", "method": "GET"},
			"variables": map[string]any{"token": map[string]any{"value": "secret", "secret": true}},
			"mapping":   map[string]any{"remaining": map[string]any{"path": "balance"}},
		},
	}
	credentials := map[string]any{}
	require.NoError(t, ProtectUpstreamUsageQuerySecrets(extra, credentials))
	require.Equal(t, "secret", credentials[UpstreamBillingUsageQuerySecretsKey].(map[string]any)["token"])
	config := extra[UpstreamBillingUsageQueryConfigExtraKey].(*UpstreamUsageQueryConfig)
	require.Empty(t, config.Variables["token"].Value)
}
