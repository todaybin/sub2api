//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationAdminCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &panelRateLimitSettingRepo{}
	svc := newPanelRateLimitTestService(repo)
	masked, exists, err := svc.GetIntegrationAdminCredentialsStatus(ctx)
	require.NoError(t, err)
	require.False(t, exists)
	require.Empty(t, masked)
	first, err := svc.GenerateIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	require.Regexp(t, `^[0-9a-f]{32}$`, first.AppID)
	require.Regexp(t, `^[0-9a-f]{64}$`, first.Secret)
	require.True(t, first.Enabled)
	loaded, err := svc.GetIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	require.Equal(t, first, loaded)
	masked, exists, err = svc.GetIntegrationAdminCredentialsStatus(ctx)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, first.AppID[:8]+"..."+first.AppID[28:], masked)
	require.NotContains(t, masked, first.Secret)
	var saved map[string]any
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyIntegrationAdminCredentials]), &saved))
	require.ElementsMatch(t, []string{"appid", "secret", "enabled"}, mapKeys(saved))
	second, err := svc.GenerateIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	require.NotEqual(t, first.AppID, second.AppID)
	require.NotEqual(t, first.Secret, second.Secret)
	require.NoError(t, svc.DeleteIntegrationAdminCredentials(ctx))
	loaded, err = svc.GetIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	require.Nil(t, loaded)
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestIntegrationAdminLegacyCredentialsStayDisabledWithoutWrites(t *testing.T) {
	for _, raw := range []string{
		`{"integration_id":"int_0123456789abcdef0123456789abcdef","signing_secret":"sec_old","enabled":true}`,
		`{"integration_id":null,"appid":"0123456789abcdef0123456789abcdef","secret":"` + strings.Repeat("a", 64) + `","enabled":true}`,
		`{"signing_secret":"old"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			repo := &panelRateLimitSettingRepo{values: map[string]string{SettingKeyIntegrationAdminCredentials: raw}}
			svc := newPanelRateLimitTestService(repo)
			credentials, err := svc.GetIntegrationAdminCredentials(context.Background())
			require.NoError(t, err)
			require.Nil(t, credentials)
			masked, exists, err := svc.GetIntegrationAdminCredentialsStatus(context.Background())
			require.NoError(t, err)
			require.False(t, exists)
			require.Empty(t, masked)
			require.Equal(t, raw, repo.values[SettingKeyIntegrationAdminCredentials])
		})
	}
}

func TestIntegrationAdminRejectsMalformedNewCredentials(t *testing.T) {
	for _, raw := range []string{
		`{`, `{}`, `{"appid":"int_old","secret":"sec_old","enabled":true}`,
		`{"appid":"` + strings.Repeat("A", 32) + `","secret":"` + strings.Repeat("a", 64) + `","enabled":true}`,
		`{"appid":"` + strings.Repeat("a", 32) + `","secret":"short","enabled":true}`,
	} {
		repo := &panelRateLimitSettingRepo{values: map[string]string{SettingKeyIntegrationAdminCredentials: raw}}
		credentials, err := newPanelRateLimitTestService(repo).GetIntegrationAdminCredentials(context.Background())
		require.Error(t, err)
		require.Nil(t, credentials)
		require.Equal(t, raw, repo.values[SettingKeyIntegrationAdminCredentials])
	}
}
