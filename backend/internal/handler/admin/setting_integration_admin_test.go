//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type integrationCredentialsSettingRepo struct {
	service.SettingRepository
	values map[string]string
}

func (r *integrationCredentialsSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *integrationCredentialsSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *integrationCredentialsSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestIntegrationAdminSettingsResponseContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &integrationCredentialsSettingRepo{values: map[string]string{}}
	handler := &SettingHandler{settingService: service.NewSettingService(repo, &config.Config{})}
	router := gin.New()
	router.GET("/settings/integration-admin", handler.GetIntegrationAdminCredentials)
	router.POST("/settings/integration-admin/regenerate", handler.RegenerateIntegrationAdminCredentials)
	router.DELETE("/settings/integration-admin", handler.DeleteIntegrationAdminCredentials)
	request := func(method, path string) map[string]any {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		require.Equal(t, http.StatusOK, w.Code)
		var response struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		return response.Data
	}
	status := request(http.MethodGet, "/settings/integration-admin")
	require.Equal(t, map[string]any{"exists": false, "masked_appid": ""}, status)
	generated := request(http.MethodPost, "/settings/integration-admin/regenerate")
	require.Len(t, generated, 2)
	require.Regexp(t, `^[0-9a-f]{32}$`, generated["appid"])
	require.Regexp(t, `^[0-9a-f]{64}$`, generated["secret"])
	appid := generated["appid"].(string)
	status = request(http.MethodGet, "/settings/integration-admin")
	require.Equal(t, map[string]any{"exists": true, "masked_appid": appid[:8] + "..." + appid[28:]}, status)
	request(http.MethodDelete, "/settings/integration-admin")
	status = request(http.MethodGet, "/settings/integration-admin")
	require.Equal(t, map[string]any{"exists": false, "masked_appid": ""}, status)
}

func TestAdminActorScopeUsesAppID(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	require.Equal(t, "admin:42", adminActorScope(c))
	c.Set(middleware.IntegrationAppIDContextKey, "0123456789abcdef0123456789abcdef")
	require.Equal(t, "integration:0123456789abcdef0123456789abcdef", adminActorScope(c))
}
