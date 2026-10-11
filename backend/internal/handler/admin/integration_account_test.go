//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type generatedAccountStub struct {
	*stubAdminService
	mu         sync.Mutex
	inputs     []service.CreateUserInput
	collisions int
	deleted    []int64
}

func (s *generatedAccountStub) CreateUser(_ context.Context, input *service.CreateUserInput) (*service.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, *input)
	if len(s.inputs) <= s.collisions {
		return nil, service.ErrEmailExists
	}
	return &service.User{ID: int64(100 + len(s.inputs)), Email: input.Email, Username: input.Username,
		ExternalID: input.ExternalID, Notes: input.Notes, Role: input.Role, Status: service.StatusActive}, nil
}

func (s *generatedAccountStub) DeleteUser(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func setupIntegrationIdempotency(t *testing.T) {
	t.Helper()
	previous := service.DefaultIdempotencyCoordinator()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ResponseEncryptionSecret = "integration-account-test-secret"
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })
}

func markIntegration(c *gin.Context) {
	c.Set(middleware.IntegrationAuthenticatedContextKey, true)
	c.Set(middleware.IntegrationAppIDContextKey, "test-app")
}

func accountRequest(r *gin.Engine, path, body, key string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	r.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationAccountCreatesCredentialsAndKeepsOpaqueMetadata(t *testing.T) {
	setupIntegrationIdempotency(t)
	admin := &generatedAccountStub{stubAdminService: newStubAdminService()}
	h := &UserHandler{adminService: admin}
	r := gin.New()
	r.Use(markIntegration)
	r.POST("/users", h.Create)
	external := " 外部编号]\n[integration_external_id:other 💬 "
	body, err := json.Marshal(map[string]any{"external_id": external, "notes": "  自定义备注\n原样保留  "})
	require.NoError(t, err)
	first := accountRequest(r, "/users", string(body), "create-1")
	require.Equal(t, 200, first.Code, first.Body.String())
	retry := accountRequest(r, "/users", string(body), "create-1")
	require.Equal(t, 200, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), retry.Body.String())
	require.Len(t, admin.inputs, 1)
	input := admin.inputs[0]
	require.Regexp(t, `^[a-f0-9]{32}ai@zoomtk\.com$`, input.Email)
	require.Equal(t, strings.TrimSuffix(input.Email, "@zoomtk.com"), input.Username)
	require.GreaterOrEqual(t, len(input.Password), 40)
	require.Equal(t, external, input.ExternalID)
	require.Equal(t, "  自定义备注\n原样保留  ", input.Notes)
	require.Equal(t, service.RoleUser, input.Role)
	require.NotContains(t, first.Body.String(), input.Password)
	require.NotContains(t, first.Body.String(), "password")
	second := accountRequest(r, "/users", string(body), "create-2")
	require.Equal(t, 200, second.Code, second.Body.String())
	require.Len(t, admin.inputs, 2)
	require.NotEqual(t, input.Email, admin.inputs[1].Email)
	require.Zero(t, admin.lastListUsers.calls)
	conflict := accountRequest(r, "/users", `{"notes":"changed"}`, "create-1")
	require.Equal(t, 409, conflict.Code)
	require.Len(t, admin.inputs, 2)
	legacy := accountRequest(r, "/users", `{"email":"invalid","password":"x","username":"caller","role":"admin"}`, "legacy")
	require.Equal(t, 200, legacy.Code, legacy.Body.String())
	require.NotEqual(t, "invalid", admin.inputs[2].Email)
	require.NotEqual(t, "caller", admin.inputs[2].Username)
	require.Equal(t, service.RoleUser, admin.inputs[2].Role)
}

func TestIntegrationAccountEmailCollisionRetriesAreBounded(t *testing.T) {
	for _, collisions := range []int{1, 4} {
		admin := &generatedAccountStub{stubAdminService: newStubAdminService(), collisions: collisions}
		user, err := createIntegrationAccount(context.Background(), admin, service.CreateUserInput{ExternalID: "same"})
		if collisions == 4 {
			require.ErrorIs(t, err, service.ErrEmailExists)
			require.Nil(t, user)
		} else {
			require.NoError(t, err)
		}
		require.Len(t, admin.inputs, min(collisions+1, 4))
		for i := 1; i < len(admin.inputs); i++ {
			require.NotEqual(t, admin.inputs[i-1].Email, admin.inputs[i].Email)
		}
	}
}

type failingProvisionKeyStub struct{ provisioningAPIKeyServiceStub }

func (s *failingProvisionKeyStub) Create(context.Context, int64, service.CreateAPIKeyRequest) (*service.APIKey, error) {
	return nil, errors.New("key creation failed")
}

func TestIntegrationProvisionGeneratesAccountAndRollsBackFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[fail], func(t *testing.T) {
			setupIntegrationIdempotency(t)
			admin := &generatedAccountStub{stubAdminService: newStubAdminService()}
			keys := &provisioningAPIKeyServiceStub{groups: []service.Group{{ID: 1}}}
			var keyService provisioningAPIKeyService = keys
			if fail {
				keyService = &failingProvisionKeyStub{provisioningAPIKeyServiceStub: *keys}
			}
			h := &ProvisioningHandler{adminService: admin, apiKeyService: keyService, subscriptionService: &service.SubscriptionService{}}
			r := gin.New()
			r.Use(markIntegration)
			r.POST("/provision", h.Create)
			body := `{"external_id":"自由编号]重复","notes":"订单备注","api_key":{"name":"default"}}`
			first := accountRequest(r, "/provision", body, "provision-1")
			require.Len(t, admin.inputs, 1)
			require.Equal(t, "订单备注", admin.inputs[0].Notes)
			require.Zero(t, admin.lastListUsers.calls)
			if fail {
				require.GreaterOrEqual(t, first.Code, 400)
				require.Equal(t, []int64{101}, admin.deleted)
				return
			}
			require.Equal(t, 200, first.Code, first.Body.String())
			retry := accountRequest(r, "/provision", body, "provision-1")
			require.Equal(t, 200, retry.Code, retry.Body.String())
			require.JSONEq(t, first.Body.String(), retry.Body.String())
			require.Len(t, admin.inputs, 1)
			require.Len(t, keys.created, 1)
		})
	}
}

func TestIntegrationAccountConcurrentRetriesCreateOnce(t *testing.T) {
	setupIntegrationIdempotency(t)
	admin := &generatedAccountStub{stubAdminService: newStubAdminService()}
	h := &UserHandler{adminService: admin}
	r := gin.New()
	r.Use(markIntegration)
	r.POST("/users", h.Create)
	var wg sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 8)
	for i := range responses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); responses[i] = accountRequest(r, "/users", `{}`, "concurrent-create") }(i)
	}
	wg.Wait()
	for _, rec := range responses {
		require.Contains(t, []int{200, 409}, rec.Code, rec.Body.String())
	}
	retry := accountRequest(r, "/users", `{}`, "concurrent-create")
	require.Equal(t, 200, retry.Code, retry.Body.String())
	require.Len(t, admin.inputs, 1)
}
