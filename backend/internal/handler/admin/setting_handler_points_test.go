//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPointsSettings_RatePersistsOnPartialUpdatesAndValidatesPrecision(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyPointsPerUSD: "100.00000000"})
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "Points Gateway"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "100.00000000", repo.values[service.SettingKeyPointsPerUSD])
	rec = doUpdateSettings(t, h, map[string]any{"points_per_usd": 250.123456789}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "250.12345679", repo.values[service.SettingKeyPointsPerUSD])
	for _, rate := range []float64{-1, 0.000000001, 1000000000000} {
		rec = doUpdateSettings(t, h, map[string]any{"points_per_usd": rate}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "250.12345679", repo.values[service.SettingKeyPointsPerUSD])
	}
	rec = doUpdateSettings(t, h, map[string]any{"points_per_usd": 0}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "0.00000000", repo.values[service.SettingKeyPointsPerUSD])
}
