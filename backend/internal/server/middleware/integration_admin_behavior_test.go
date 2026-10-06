//go:build unit || integration

package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type integrationAdminUserRepo struct {
	service.UserRepository
	admin *service.User
}

func (r *integrationAdminUserRepo) GetFirstAdmin(context.Context) (*service.User, error) {
	return r.admin, nil
}

type integrationFailingSettingRepo struct {
	*panelRateLimitStubRepo
	failKey string
}

func (r *integrationFailingSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == r.failKey {
		return "", errors.New("storage unavailable")
	}
	return r.panelRateLimitStubRepo.GetValue(ctx, key)
}

// This suite runs unchanged against miniredis in unit tests and a real Redis
// server in integration tests, exercising the actual gateway dispatch.
func testIntegrationAdminGateway(t *testing.T, client *redis.Client, unavailable bool) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	repo := &integrationFailingSettingRepo{panelRateLimitStubRepo: &panelRateLimitStubRepo{}}
	settings := service.NewSettingService(repo, &config.Config{})
	key, err := settings.GenerateAdminAPIKey(ctx)
	require.NoError(t, err)
	credentials, err := settings.GenerateIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	admin := &service.User{ID: 42, Role: service.RoleAdmin, Status: service.StatusActive, Concurrency: 3}
	users := service.NewUserService(&integrationAdminUserRepo{admin: admin}, nil, nil, nil)
	router := gin.New()
	handlerCalls := 0
	router.POST("/api/v1/admin/users/provision", adminAuth(nil, users, settings, nil), func(c *gin.Context) {
		handlerCalls++
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, `/api/v1/admin/users/provision`, c.Request.URL.Path)
		require.Equal(t, "page=2&filter=a%2Bb", c.Request.URL.RawQuery)
		require.Equal(t, `{"email":"alice@example.com"}`, string(body))
		require.Equal(t, credentials.AppID, c.GetString(IntegrationAppIDContextKey))
		require.True(t, c.GetBool(IntegrationAuthenticatedContextKey))
		require.Equal(t, "integration:"+credentials.AppID, c.GetString(ContextKeyAuthEmail))
		require.Equal(t, "integration", c.GetString("auth_method"))
		require.Equal(t, AuthSubject{UserID: 42, Concurrency: 3}, c.MustGet(string(ContextKeyUser)))
		require.Equal(t, "business-1001", c.GetHeader("Idempotency-Key"))
		c.Header("X-Business-Result", "preserved")
		c.JSON(http.StatusCreated, gin.H{"created": true})
	})
	router.Any("/api/v1/integrations/admin/*path", NewIntegrationAdminGateway(router, settings, users, client))
	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/admin/users/provision?page=2&filter=a%2Bb", strings.NewReader(`{"email":"alice@example.com"}`))
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := strconv.FormatInt(time.Now().UnixNano(), 10)
		req.Header.Set("x-api-key", key)
		req.Header.Set("X-App-Id", credentials.AppID)
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Nonce", nonce)
		req.Header.Set("Idempotency-Key", "business-1001")
		req.Header.Set("Content-Type", "application/json")
		mac := hmac.New(sha256.New, []byte(credentials.Secret))
		_, _ = mac.Write([]byte(IntegrationCanonicalString(http.MethodPost, "/api/v1/admin/users/provision", credentials.AppID, timestamp, nonce, []byte(`{"email":"alice@example.com"}`))))
		req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
		if client != nil {
			hash := sha256.Sum256([]byte(credentials.AppID + "\n" + nonce))
			t.Cleanup(func() { _ = client.Del(ctx, "integration:admin:nonce:"+hex.EncodeToString(hash[:])).Err() })
		}
		return req
	}
	dispatch := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	for _, header := range []string{"x-api-key", "X-App-Id", "X-Timestamp", "X-Nonce", "X-Signature"} {
		t.Run("missing_"+header, func(t *testing.T) {
			req := newRequest()
			req.Header.Del(header)
			w := dispatch(req)
			require.Equal(t, 401, w.Code)
			require.Contains(t, w.Body.String(), "INTEGRATION_AUTH_FAILED")
		})
	}
	tests := []struct {
		name, code string
		status     int
		mutate     func(*http.Request)
	}{
		{"wrong_api_key", "INTEGRATION_AUTH_FAILED", 401, func(r *http.Request) { r.Header.Set("x-api-key", "wrong") }},
		{"wrong_appid", "INTEGRATION_AUTH_FAILED", 401, func(r *http.Request) { r.Header.Set("X-App-Id", strings.Repeat("0", 32)) }},
		{"legacy_header", "INTEGRATION_AUTH_FAILED", 401, func(r *http.Request) { r.Header.Set("X-Integration-ID", "int_old") }},
		{"bad_signature", "INTEGRATION_SIGNATURE_INVALID", 401, func(r *http.Request) { r.Header.Set("X-Signature", strings.Repeat("0", 64)) }},
		{"tampered_body", "INTEGRATION_SIGNATURE_INVALID", 401, func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"email":"mallory@example.com"}`)) }},
		{"tampered_method", "INTEGRATION_SIGNATURE_INVALID", 401, func(r *http.Request) { r.Method = http.MethodPut }},
		{"tampered_path", "INTEGRATION_SIGNATURE_INVALID", 401, func(r *http.Request) { r.URL.Path = "/api/v1/integrations/admin/users" }},
		{"expired_timestamp", "INTEGRATION_TIMESTAMP_EXPIRED", 401, func(r *http.Request) {
			r.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Add(-6*time.Minute).Unix(), 10))
		}},
		{"future_timestamp", "INTEGRATION_TIMESTAMP_EXPIRED", 401, func(r *http.Request) {
			r.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Add(6*time.Minute).Unix(), 10))
		}},
		{"invalid_timestamp", "INTEGRATION_TIMESTAMP_EXPIRED", 401, func(r *http.Request) { r.Header.Set("X-Timestamp", "bad") }},
		{"oversized_nonce", "INTEGRATION_AUTH_FAILED", 401, func(r *http.Request) { r.Header.Set("X-Nonce", strings.Repeat("n", 129)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest()
			tc.mutate(req)
			w := dispatch(req)
			require.Equal(t, tc.status, w.Code)
			require.Contains(t, w.Body.String(), tc.code)
		})
	}
	for _, path := range []string{"/settings/admin-api-key", "/settings/integration-admin/regenerate", "/backups/1", "/system/restart", "/audit-logs/clear"} {
		req := newRequest()
		req.URL.Path = "/api/v1/integrations/admin" + path
		w := dispatch(req)
		require.Equal(t, 403, w.Code)
		require.Contains(t, w.Body.String(), "INTEGRATION_ROUTE_FORBIDDEN")
	}
	for _, failKey := range []string{service.SettingKeyAdminAPIKey, service.SettingKeyIntegrationAdminCredentials} {
		repo.failKey = failKey
		w := dispatch(newRequest())
		require.Equal(t, 503, w.Code)
		require.Contains(t, w.Body.String(), "INTEGRATION_AUTH_UNAVAILABLE")
	}
	repo.failKey = ""
	for _, suppliedKey := range []string{"wrong", ""} {
		require.NoError(t, repo.Set(ctx, service.SettingKeyAdminAPIKey, suppliedKey))
		w := dispatch(newRequest())
		require.Equal(t, 401, w.Code)
	}
	require.NoError(t, repo.Set(ctx, service.SettingKeyAdminAPIKey, key))
	// Even a valid gateway signature cannot authorize a direct admin request.
	direct := newRequest()
	direct.URL.Path = "/api/v1/admin/users/provision"
	wDirect := dispatch(direct)
	require.Equal(t, 401, wDirect.Code)
	require.Contains(t, wDirect.Body.String(), "ADMIN_API_KEY_GATEWAY_REQUIRED")
	require.Zero(t, handlerCalls, "authentication failures must never execute business handlers")
	if client == nil || unavailable {
		w := dispatch(newRequest())
		require.Equal(t, 503, w.Code)
		require.Contains(t, w.Body.String(), "INTEGRATION_NONCE_STORE_UNAVAILABLE")
		return
	}
	req := newRequest()
	replay := newRequest()
	replay.Header = req.Header.Clone()
	w := dispatch(req)
	require.Equal(t, 201, w.Code)
	require.JSONEq(t, `{"created":true}`, w.Body.String())
	require.Equal(t, "preserved", w.Header().Get("X-Business-Result"))
	w = dispatch(replay)
	require.Equal(t, 409, w.Code)
	require.Contains(t, w.Body.String(), "INTEGRATION_NONCE_REPLAYED")
	require.Equal(t, 1, handlerCalls)
	// Redis must retain the nonce with a bounded expiration.
	hash := sha256.Sum256([]byte(credentials.AppID + "\n" + req.Header.Get("X-Nonce")))
	ttl, err := client.TTL(ctx, "integration:admin:nonce:"+hex.EncodeToString(hash[:])).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, 5*time.Minute)
	// A fresh nonce reaches the business layer even with the same idempotency key.
	w = dispatch(newRequest())
	require.Equal(t, 201, w.Code)
	oldRequest := newRequest()
	credentials, err = settings.GenerateIntegrationAdminCredentials(ctx)
	require.NoError(t, err)
	w = dispatch(oldRequest)
	require.Equal(t, 401, w.Code)
	w = dispatch(newRequest())
	require.Equal(t, 201, w.Code)
	// Disabled new-format credentials cannot authenticate.
	raw, err := repo.GetValue(ctx, service.SettingKeyIntegrationAdminCredentials)
	require.NoError(t, err)
	require.NoError(t, repo.Set(ctx, service.SettingKeyIntegrationAdminCredentials, strings.Replace(raw, `"enabled":true`, `"enabled":false`, 1)))
	w = dispatch(newRequest())
	require.Equal(t, 401, w.Code)
	require.NoError(t, repo.Set(ctx, service.SettingKeyIntegrationAdminCredentials, raw))
	require.NoError(t, settings.DeleteIntegrationAdminCredentials(ctx))
	w = dispatch(newRequest())
	require.Equal(t, 401, w.Code)
	require.NoError(t, repo.Set(ctx, service.SettingKeyIntegrationAdminCredentials, `{"integration_id":"int_old","signing_secret":"sec_old","enabled":true}`))
	w = dispatch(newRequest())
	require.Equal(t, 401, w.Code)
}
