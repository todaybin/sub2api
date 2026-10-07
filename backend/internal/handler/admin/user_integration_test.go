//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type integrationKeyStub struct {
	provisioningAPIKeyServiceStub
	key *service.APIKey
}

func (s *integrationKeyStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	return s.key, nil
}

type integrationUsageStub struct {
	filters usagestats.UsageLogFilters
	calls   int
}

func (s *integrationUsageStub) GetStatsWithFilters(_ context.Context, f usagestats.UsageLogFilters) (*usagestats.UsageStats, error) {
	s.filters = f
	s.calls++
	return &usagestats.UsageStats{TotalRequests: 2, TotalInputTokens: 100, TotalOutputTokens: 25, TotalCacheTokens: 10, TotalTokens: 135, TotalActualCost: 0.12}, nil
}

type integrationAuthStub struct {
	user    *service.User
	binding *service.SessionBinding
	calls   int
}

func (s *integrationAuthStub) GenerateTokenPair(ctx context.Context, u *service.User, _ string) (*service.TokenPair, error) {
	s.calls++
	s.user = u
	s.binding = service.SessionBindingFromContext(ctx)
	return &service.TokenPair{AccessToken: "jwt", RefreshToken: "refresh", ExpiresIn: 600}, nil
}

type integrationModelsStub struct{ calls []int64 }

func (s *integrationModelsStub) SmartModelCatalog(_ context.Context, g *service.Group) ([]string, error) {
	s.calls = append(s.calls, g.ID)
	if !g.IsActive() {
		return nil, nil
	}
	if g.ID == 1 {
		return []string{"shared", "a", "shared"}, nil
	}
	return []string{"shared", "b"}, nil
}

func integrationRouter(h *UserHandler, middlewareHandlers ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middlewareHandlers...)
	r.GET("/users/:id/usage", h.GetUserUsage)
	r.POST("/users/:id/api-keys", h.CreateUserAPIKey)
	r.POST("/users/:id/token", func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithSessionBinding(c.Request.Context(), &service.SessionBinding{IP: "192.0.2.8", UserAgent: "integration-server"}))
		h.IssueUserToken(c)
	})
	r.GET("/users/:id/models", h.GetUserModels)
	r.GET("/groups/models", h.GetGroupModels)
	return r
}
func integrationRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationCreateKeyDefaultsAndExplicitGroup(t *testing.T) {
	for _, tt := range []struct {
		body   string
		mode   string
		status int
	}{
		{`{"name":"default"}`, "smart", 200},
		{`{"name":"sequential","routing_strategy":"sequential","smart_group_ids":[2,1]}`, "smart", 200},
		{`{"name":"sequential-missing","routing_strategy":"sequential"}`, "", 400},
		{`{"name":"single","group_id":2}`, "single", 200},
		{`{"name":"restricted","smart_group_ids":[2,2],"routing_strategy":"price"}`, "smart", 200},
		{`{"name":"bad","group_id":0}`, "", 400},
		{`{"name":"bad","group_id":2,"routing_mode":"smart"}`, "", 400},
		{`{"name":"bad","group_id":2,"smart_group_ids":[1]}`, "", 400},
		{`{"name":"bad","routing_mode":"single"}`, "", 400},
		{`{"name":"bad","smart_group_ids":[-1]}`, "", 400},
		{`{"name":"bad","quota":-1}`, "", 400},
	} {
		t.Run(tt.body, func(t *testing.T) {
			keys := &integrationKeyStub{provisioningAPIKeyServiceStub: provisioningAPIKeyServiceStub{groups: []service.Group{{ID: 1}, {ID: 2}}}}
			h := &UserHandler{adminService: newStubAdminService(), integrationKeys: keys}
			rec := integrationRequest(integrationRouter(h), "POST", "/users/1/api-keys", tt.body)
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			if tt.status == 200 {
				require.Len(t, keys.created, 1)
				require.Equal(t, tt.mode, keys.created[0].RoutingMode)
				if tt.mode == "smart" {
					require.Nil(t, keys.created[0].GroupID)
					require.NotEmpty(t, keys.created[0].SmartGroupIDs)
					if keys.created[0].RoutingStrategy == "sequential" {
						require.Equal(t, []int64{2, 1}, keys.created[0].SmartGroupIDs)
					}
				}
			} else {
				require.Empty(t, keys.created)
			}
		})
	}
	keys := &integrationKeyStub{}
	rec := integrationRequest(integrationRouter(&UserHandler{adminService: newStubAdminService(), integrationKeys: keys}), "POST", "/users/1/api-keys", `{"name":"default"}`)
	require.Equal(t, 400, rec.Code)
	require.Empty(t, keys.created)
}

func TestIntegrationUsageActualStatsAndUserIsolation(t *testing.T) {
	usage := &integrationUsageStub{}
	keys := &integrationKeyStub{key: &service.APIKey{ID: 9, UserID: 1}}
	r := integrationRouter(&UserHandler{adminService: newStubAdminService(), integrationKeys: keys, integrationUsage: usage})
	rec := integrationRequest(r, "GET", "/users/1/usage?api_key_id=9&start_date=2026-10-01&end_date=2026-10-02&timezone=Asia%2FShanghai", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"total_tokens":135`)
	require.Contains(t, rec.Body.String(), `"total_actual_cost":0.12`)
	require.Equal(t, int64(1), usage.filters.UserID)
	require.Equal(t, int64(9), usage.filters.APIKeyID)
	require.Equal(t, "2026-09-30T16:00:00Z", usage.filters.StartTime.UTC().Format(time.RFC3339))
	require.Equal(t, "2026-10-02T16:00:00Z", usage.filters.EndTime.UTC().Format(time.RFC3339))
	keys.key.UserID = 2
	rec = integrationRequest(r, "GET", "/users/1/usage?api_key_id=9", "")
	require.Equal(t, 404, rec.Code)
	require.Equal(t, 1, usage.calls)
	for _, query := range []string{"start_date=2026-10-01", "start_date=bad&end_date=bad", "start_date=2026-10-02&end_date=2026-10-01", "timezone=Invalid", "period=invalid", "start_time=2026-10-01T00:00:00Z", "start_time=2026-10-01T00:00:00Z&end_time=2026-10-01T00:00:00Z", "api_key_id=0"} {
		rec = integrationRequest(r, "GET", "/users/1/usage?"+query, "")
		require.Equal(t, 400, rec.Code, query)
	}
	rec = integrationRequest(r, "GET", "/users/0/usage", "")
	require.Equal(t, 400, rec.Code)
	rec = integrationRequest(r, "GET", "/users/1/usage?start_time=2026-10-01T00:00:00Z&end_time=2026-10-01T01:00:00Z", "")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, time.Hour, usage.filters.EndTime.Sub(*usage.filters.StartTime))
}

func TestIntegrationLoginTokenGuardsAndBinding(t *testing.T) {
	for _, tt := range []struct {
		role, status string
		totp         bool
		body         string
		code         int
	}{
		{service.RoleUser, service.StatusActive, false, `{}`, 200},
		{service.RoleUser, service.StatusActive, false, `{"client_ip":"203.0.113.5","user_agent":"browser"}`, 200},
		{service.RoleAdmin, service.StatusActive, false, `{}`, 403},
		{service.RoleUser, service.StatusDisabled, false, `{}`, 403},
		{service.RoleUser, service.StatusActive, true, `{}`, 403},
		{service.RoleUser, service.StatusActive, false, `{"client_ip":"bad","user_agent":"browser"}`, 400},
		{service.RoleUser, service.StatusActive, false, `{"client_ip":"203.0.113.5"}`, 400},
	} {
		t.Run(tt.role+tt.status+tt.body, func(t *testing.T) {
			admin := newStubAdminService()
			admin.users = []service.User{{ID: 1, Role: tt.role, Status: tt.status, TotpEnabled: tt.totp}}
			auth := &integrationAuthStub{}
			rec := integrationRequest(integrationRouter(&UserHandler{adminService: admin, integrationAuth: auth}), http.MethodPost, "/users/1/token", tt.body)
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
			if tt.code == 200 {
				require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
				require.Contains(t, rec.Body.String(), `"refresh_token":"refresh"`)
				require.NotEqual(t, "192.0.2.8", auth.binding.IP)
			} else {
				require.Zero(t, auth.calls)
			}
		})
	}
}

func TestIntegrationModelsDeduplicateAndRespectGroupsAndKey(t *testing.T) {
	admin := newStubAdminService()
	admin.groups = []service.Group{{ID: 1, Status: service.StatusActive}, {ID: 2, Status: service.StatusActive}, {ID: 3, Status: service.StatusDisabled}}
	models := &integrationModelsStub{}
	keys := &integrationKeyStub{provisioningAPIKeyServiceStub: provisioningAPIKeyServiceStub{groups: admin.groups}, key: &service.APIKey{ID: 9, UserID: 1, RoutingMode: "smart", SmartGroupIDs: []int64{2}}}
	r := integrationRouter(&UserHandler{adminService: admin, integrationKeys: keys, integrationModels: models})
	rec := integrationRequest(r, "GET", "/groups/models", "")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"id":"shared"`))
	require.Contains(t, rec.Body.String(), `"group_ids":[1,2]`)
	models.calls = nil
	rec = integrationRequest(r, "GET", "/groups/models?group_ids=1", "")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, []int64{1}, models.calls)
	require.NotContains(t, rec.Body.String(), `"id":"b"`)
	rec = integrationRequest(r, "GET", "/groups/models?group_ids=100", "")
	require.Equal(t, 404, rec.Code)
	rec = integrationRequest(r, "GET", "/groups/models?group_ids=0", "")
	require.Equal(t, 400, rec.Code)
	models.calls = nil
	rec = integrationRequest(r, "GET", "/users/1/models?api_key_id=9", "")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, []int64{2}, models.calls)
	keys.key.UserID = 2
	rec = integrationRequest(r, "GET", "/users/1/models?api_key_id=9", "")
	require.Equal(t, 404, rec.Code)
}

func TestProvisionDefaultsSmartWithoutReusingLegacyUnboundKey(t *testing.T) {
	admin := newStubAdminService()
	admin.apiKeys = []service.APIKey{{ID: 10, UserID: 1, Status: service.StatusActive, Key: "old-unbound"}}
	keys := &provisioningAPIKeyServiceStub{groups: []service.Group{{ID: 1}, {ID: 2}}}
	h := &ProvisioningHandler{adminService: admin, apiKeyService: keys}
	out, ids, err := h.ensureAPIKeys(context.Background(), 1, []ProvisioningAPIKeyRequest{{Name: "smart"}})
	require.NoError(t, err)
	require.Len(t, ids, 1)
	require.Equal(t, "smart", out[0].RoutingMode)
	require.Equal(t, []int64{1, 2}, out[0].SmartGroupIDs)
	admin.apiKeys = append(admin.apiKeys, service.APIKey{ID: 11, UserID: 1, Status: service.StatusActive, RoutingMode: "smart", SmartGroupIDs: []int64{2, 1}, Key: "old-smart"})
	out, ids, err = h.ensureAPIKeys(context.Background(), 1, []ProvisioningAPIKeyRequest{{Name: "smart"}})
	require.NoError(t, err)
	require.Empty(t, ids)
	require.Empty(t, out[0].Key)
	require.False(t, out[0].Created)
}

func TestIntegrationWritesIdempotentRetriesAndConflicts(t *testing.T) {
	repo := newMemoryIdempotencyRepoStub()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, func() service.IdempotencyConfig {
		cfg := service.DefaultIdempotencyConfig()
		cfg.ResponseEncryptionSecret = "test-persistent-jwt-secret"
		return cfg
	}()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	admin := &provisioningAdminServiceStub{stubAdminService: newStubAdminService()}
	keys := &integrationKeyStub{provisioningAPIKeyServiceStub: provisioningAPIKeyServiceStub{groups: []service.Group{{ID: 1}}}}
	auth := &integrationAuthStub{}
	h := &UserHandler{adminService: admin, integrationKeys: keys, integrationAuth: auth}
	r := integrationRouter(h, func(c *gin.Context) { c.Set(middleware.IntegrationAppIDContextKey, "appid") })
	r.POST("/users", func(c *gin.Context) { c.Set(middleware.IntegrationAppIDContextKey, "appid"); h.Create(c) })
	call := func(path, body, key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		r.ServeHTTP(rec, req)
		return rec
	}
	for _, item := range []struct{ path, body, key string }{
		{"/users", `{"email":"new@example.com","password":"pass123"}`, "create-user"},
		{"/users/1/api-keys", `{"name":"default"}`, "create-key"},
		{"/users/1/token", `{}`, "issue-token"},
	} {
		rec := call(item.path, item.body, "")
		require.Equal(t, 400, rec.Code, rec.Body.String())
		first := call(item.path, item.body, item.key)
		require.Equal(t, 200, first.Code, first.Body.String())
		second := call(item.path, item.body, item.key)
		require.Equal(t, 200, second.Code)
		require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
		require.JSONEq(t, first.Body.String(), second.Body.String())
	}
	require.Equal(t, 1, admin.createdUsers)
	require.Len(t, keys.created, 1)
	require.Equal(t, 1, auth.calls)
	for _, record := range repo.data {
		if record.ResponseBody != nil {
			require.NotContains(t, *record.ResponseBody, `"access_token"`)
			require.NotContains(t, *record.ResponseBody, `"refresh_token"`)
		}
	}
	rec := call("/users/1/api-keys", `{"name":"changed"}`, "create-key")
	require.Equal(t, 409, rec.Code)
	require.Len(t, keys.created, 1)
}

func TestIntegrationWritesRequireCoordinatorAndKey(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
	for _, configured := range []bool{false, true} {
		service.SetDefaultIdempotencyCoordinator(nil)
		if configured {
			service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(storeUnavailableRepoStub{}, service.DefaultIdempotencyConfig()))
		}
		executed := 0
		r := gin.New()
		r.POST("/write", func(c *gin.Context) {
			c.Set(middleware.IntegrationAppIDContextKey, "appid")
			executeAdminIdempotentJSONFailOpenOnStoreUnavailable(c, "test.integration", nil, time.Minute, func(context.Context) (any, error) {
				executed++
				return gin.H{"ok": true}, nil
			})
		})
		for _, key := range []string{"", "write-1"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/write", nil)
			req.Header.Set("Idempotency-Key", key)
			r.ServeHTTP(rec, req)
			if key == "" {
				require.Equal(t, 400, rec.Code)
			} else {
				require.Equal(t, 503, rec.Code)
			}
		}
		require.Zero(t, executed)
	}
}
