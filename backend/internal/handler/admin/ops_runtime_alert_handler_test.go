package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsRuntimeAlertSettingsAvailableWhileMonitoringDisabled(t *testing.T) {
	for _, hardDisabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "database switch", true: "config switch"}[hardDisabled], func(t *testing.T) {
			repo := newTestSettingRepo()
			repo.values[service.SettingKeyOpsMonitoringEnabled] = "false"
			svc := service.NewOpsService(nil, repo, &config.Config{Ops: config.OpsConfig{Enabled: !hardDisabled}}, nil, nil, nil, nil, nil, nil, nil, nil)
			require.False(t, svc.IsMonitoringEnabled(context.Background()))
			h := NewOpsHandler(svc)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.GET("/api/v1/admin/ops/runtime/alert", h.GetAlertRuntimeSettings)
			r.PUT("/api/v1/admin/ops/runtime/alert", h.UpdateAlertRuntimeSettings)
			// The settings dialog fetches these alongside the alert settings.
			r.GET("/email", h.GetEmailNotificationConfig)
			r.GET("/advanced", h.GetAdvancedSettings)
			r.GET("/thresholds", h.GetMetricThresholds)
			r.GET("/logging", h.GetRuntimeLogConfig)

			for _, path := range []string{"/api/v1/admin/ops/runtime/alert?timezone=Asia%2FShanghai", "/email", "/advanced", "/thresholds", "/logging"} {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/runtime/alert", bytes.NewBufferString(`{"evaluation_interval_seconds":45,"enabled":true}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var saved service.OpsAlertRuntimeSettings
			require.NoError(t, json.Unmarshal([]byte(repo.values[service.SettingKeyOpsAlertRuntimeSettings]), &saved))
			require.Equal(t, 45, saved.EvaluationIntervalSeconds)
			require.False(t, svc.IsMonitoringEnabled(context.Background()), "saving alert configuration must not enable monitoring")

			w = httptest.NewRecorder()
			req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/runtime/alert", bytes.NewBufferString(`{"evaluation_interval_seconds":0}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}
