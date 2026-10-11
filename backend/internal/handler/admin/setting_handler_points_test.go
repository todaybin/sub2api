//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPointsSettings_LegacyGlobalRateIsIgnored(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyPointsPerUSD: "100.00000000"})
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "Points Gateway"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "100.00000000", repo.values[service.SettingKeyPointsPerUSD])
	for _, rate := range []float64{250.123456789, -1, 0, 0.000000001, 1000000000000} {
		rec = doUpdateSettings(t, h, map[string]any{"points_per_usd": rate}, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "100.00000000", repo.values[service.SettingKeyPointsPerUSD])
		require.NotContains(t, rec.Body.String(), `"points_per_usd"`)
	}
}
