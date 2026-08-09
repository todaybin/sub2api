package middleware

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	IntegrationAuthenticatedContextKey = "integration_admin_authenticated"
	IntegrationIDContextKey            = "integration_admin_id"
	integrationTimestampTolerance      = 5 * time.Minute
	integrationNonceTTL                = 5 * time.Minute
)

type integrationRequestContextKey struct{}

type integrationRequestIdentity struct {
	IntegrationID string
	Admin         AuthSubject
	AdminRole     string
}

// NewIntegrationAdminGateway authenticates a signed request and then dispatches
// it to the corresponding existing /api/v1/admin route. No business handler is
// duplicated for third-party callers.
func NewIntegrationAdminGateway(engine *gin.Engine, settings *service.SettingService, users *service.UserService, redisClient *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Param("path")
		if path == "" || path == "/" || strings.Contains(path, "..") || strings.Contains(path, "\\") {
			AbortWithError(c, http.StatusNotFound, "INTEGRATION_ROUTE_NOT_FOUND", "Integration route not found")
			return
		}
		targetPath := "/api/v1/admin" + path
		if integrationRouteForbidden(path) {
			AbortWithError(c, http.StatusForbidden, "INTEGRATION_ROUTE_FORBIDDEN", "This admin route is not available to integrations")
			return
		}
		integrationID, ok := verifyIntegrationRequest(c, targetPath, settings, redisClient)
		if !ok {
			return
		}
		admin, err := users.GetFirstAdmin(c.Request.Context())
		if err != nil {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No admin user found")
			return
		}

		// HandleContext resets Gin keys, so carry the verified identity in the
		// request context. adminAuth restores it for the reused admin route.
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), integrationRequestContextKey{}, integrationRequestIdentity{
			IntegrationID: integrationID,
			Admin:         AuthSubject{UserID: admin.ID, Concurrency: admin.Concurrency},
			AdminRole:     admin.Role,
		}))

		originalPath, originalRawPath := c.Request.URL.Path, c.Request.URL.RawPath
		c.Request.URL.Path = targetPath
		c.Request.URL.RawPath = ""
		engine.HandleContext(c)
		c.Request.URL.Path, c.Request.URL.RawPath = originalPath, originalRawPath
	}
}

func integrationIdentityFromRequest(ctx context.Context) (integrationRequestIdentity, bool) {
	identity, ok := ctx.Value(integrationRequestContextKey{}).(integrationRequestIdentity)
	return identity, ok && identity.IntegrationID != "" && identity.Admin.UserID > 0
}

func verifyIntegrationRequest(c *gin.Context, targetPath string, settings *service.SettingService, redisClient *redis.Client) (string, bool) {
	integrationID := strings.TrimSpace(c.GetHeader("X-Integration-ID"))
	timestampRaw := strings.TrimSpace(c.GetHeader("X-Timestamp"))
	nonce := strings.TrimSpace(c.GetHeader("X-Nonce"))
	signature := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Signature")))
	if integrationID == "" || timestampRaw == "" || nonce == "" || signature == "" {
		AbortWithError(c, http.StatusUnauthorized, "INTEGRATION_AUTH_FAILED", "Integration authentication headers are required")
		return "", false
	}
	if len(nonce) > 128 || len(signature) != sha256.Size*2 {
		AbortWithError(c, http.StatusUnauthorized, "INTEGRATION_AUTH_FAILED", "Invalid integration authentication")
		return "", false
	}
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil || time.Since(time.Unix(timestamp, 0)).Abs() > integrationTimestampTolerance {
		AbortWithError(c, http.StatusUnauthorized, "INTEGRATION_TIMESTAMP_EXPIRED", "Integration timestamp is outside the allowed window")
		return "", false
	}
	credentials, err := settings.GetIntegrationAdminCredentials(c.Request.Context())
	if err != nil || credentials == nil || !credentials.Enabled || subtle.ConstantTimeCompare([]byte(integrationID), []byte(credentials.IntegrationID)) != 1 {
		AbortWithError(c, http.StatusUnauthorized, "INTEGRATION_AUTH_FAILED", "Invalid integration authentication")
		return "", false
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		AbortWithError(c, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Unable to read request body")
		return "", false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	canonical := IntegrationCanonicalString(c.Request.Method, targetPath, integrationID, timestampRaw, nonce, body)
	mac := hmac.New(sha256.New, []byte(credentials.SigningSecret))
	_, _ = mac.Write([]byte(canonical))
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		AbortWithError(c, http.StatusUnauthorized, "INTEGRATION_SIGNATURE_INVALID", "Invalid integration signature")
		return "", false
	}
	if redisClient == nil {
		AbortWithError(c, http.StatusServiceUnavailable, "INTEGRATION_NONCE_STORE_UNAVAILABLE", "Integration replay protection is unavailable")
		return "", false
	}
	nonceHash := sha256.Sum256([]byte(integrationID + "\n" + nonce))
	accepted, err := redisClient.SetNX(c.Request.Context(), "integration:admin:nonce:"+hex.EncodeToString(nonceHash[:]), "1", integrationNonceTTL).Result()
	if err != nil {
		AbortWithError(c, http.StatusServiceUnavailable, "INTEGRATION_NONCE_STORE_UNAVAILABLE", "Integration replay protection is unavailable")
		return "", false
	}
	if !accepted {
		AbortWithError(c, http.StatusConflict, "INTEGRATION_NONCE_REPLAYED", "Integration nonce has already been used")
		return "", false
	}
	return integrationID, true
}

func integrationRouteForbidden(path string) bool {
	forbiddenPrefixes := []string{
		"/settings/admin-api-key", "/settings/integration-admin", "/backups", "/system", "/audit-logs/clear",
	}
	for _, prefix := range forbiddenPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func IntegrationCanonicalString(method, targetPath, integrationID, timestamp, nonce string, body []byte) string {
	hash := sha256.Sum256(body)
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s", method, targetPath, integrationID, timestamp, nonce, hex.EncodeToString(hash[:]))
}
