// Package middleware provides HTTP middleware for authentication, authorization, and request processing.
package middleware

import (
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// NewAdminAuthMiddleware 创建管理员认证中间件
func NewAdminAuthMiddleware(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
) AdminAuthMiddleware {
	return AdminAuthMiddleware(adminAuth(authService, userService, settingService, auditService))
}

// adminAuth 管理员认证中间件实现
// 管理员会话使用 JWT，受限密钥按范围授权；旧式密钥必须通过 API Key 和 App 签名集成网关。
func adminAuth(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The integration gateway has already authenticated a signed request and
		// installed a synthetic admin subject for the existing admin handlers.
		if identity, ok := integrationIdentityFromRequest(c.Request.Context()); ok {
			c.Set(IntegrationAuthenticatedContextKey, true)
			c.Set(IntegrationAppIDContextKey, identity.AppID)
			c.Set(string(ContextKeyUser), identity.Admin)
			c.Set(string(ContextKeyUserRole), identity.AdminRole)
			c.Set(ContextKeyAuthEmail, "integration:"+identity.AppID)
			c.Set("auth_method", "integration")
			c.Next()
			return
		}
		// WebSocket upgrade requests cannot set Authorization headers in browsers.
		// For admin WebSocket endpoints (e.g. Ops realtime), allow passing the JWT via
		// Sec-WebSocket-Protocol (subprotocol list) using a prefixed token item:
		//   Sec-WebSocket-Protocol: sub2api-admin, jwt.<token>
		if isWebSocketUpgradeRequest(c) {
			if token := extractJWTFromWebSocketSubprotocol(c); token != "" {
				if !validateJWTForAdmin(c, token, authService, userService, settingService, auditService) {
					return
				}
				c.Next()
				return
			}
		}

		// 受限管理密钥沿用上游的逐请求授权和审计，不能获得旧式密钥的全局权限。
		if apiKey := c.GetHeader("x-api-key"); strings.HasPrefix(apiKey, "adm_sc_") {
			authenticateScopedAdminRequest(c, apiKey, settingService, userService, auditService)
			return
		}

		// 旧式管理密钥必须经过签名网关，不能回退到 JWT 认证。
		if _, supplied := c.Request.Header["X-Api-Key"]; supplied {
			AbortWithError(c, 401, "ADMIN_API_KEY_GATEWAY_REQUIRED", "Admin API key authentication requires the integration gateway and App signature")
			return
		}

		// 检查 Authorization header（JWT 认证）
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token := strings.TrimSpace(parts[1])
				if token == "" {
					AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
					return
				}
				if !validateJWTForAdmin(c, token, authService, userService, settingService, auditService) {
					return
				}
				c.Next()
				return
			}
		}

		// 无有效认证信息
		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
	}
}

func isWebSocketUpgradeRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// RFC6455 handshake uses:
	//   Connection: Upgrade
	//   Upgrade: websocket
	upgrade := strings.ToLower(strings.TrimSpace(c.GetHeader("Upgrade")))
	if upgrade != "websocket" {
		return false
	}
	connection := strings.ToLower(c.GetHeader("Connection"))
	return strings.Contains(connection, "upgrade")
}

func extractJWTFromWebSocketSubprotocol(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw := strings.TrimSpace(c.GetHeader("Sec-WebSocket-Protocol"))
	if raw == "" {
		return ""
	}

	// The header is a comma-separated list of tokens. We reserve the prefix "jwt."
	// for carrying the admin JWT.
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "jwt.") {
			token := strings.TrimSpace(strings.TrimPrefix(p, "jwt."))
			if token != "" {
				return token
			}
		}
	}
	return ""
}

// validateJWTForAdmin 验证 JWT 并检查管理员权限
func validateJWTForAdmin(
	c *gin.Context,
	token string,
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
) bool {
	// 验证 JWT token
	claims, err := authService.ValidateToken(token)
	if err != nil {
		if errors.Is(err, service.ErrTokenExpired) {
			AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
			return false
		}
		AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
		return false
	}

	// 从数据库获取用户
	user, err := userService.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
		} else {
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to load user")
		}
		return false
	}

	// 检查用户状态
	if !user.IsActive() {
		AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
		return false
	}

	// 校验 TokenVersion，确保管理员改密后旧 token 失效
	if claims.TokenVersion != user.TokenVersion {
		AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
		return false
	}

	// 会话绑定校验：IP/UA 任一变化即撤销会话（功能可在系统设置中关闭）
	if !enforceSessionBinding(c, authService, settingService, auditService, claims) {
		return false
	}

	// 检查管理员权限
	if !user.IsAdmin() {
		AbortWithError(c, 403, "FORBIDDEN", "Admin access required")
		return false
	}

	c.Set(string(ContextKeyUser), AuthSubject{
		UserID:      user.ID,
		Concurrency: user.Concurrency,
	})
	c.Set(string(ContextKeyUserRole), user.Role)
	c.Set(ContextKeyAuthEmail, user.Email)
	c.Set(ContextKeySessionID, claims.SessionID)
	c.Set("auth_method", "jwt")

	return true
}

// 每次请求查库，未登记的管理动作默认拒绝。
func authenticateScopedAdminRequest(c *gin.Context, key string, settings *service.SettingService, users *service.UserService, audit *service.AuditLogService) {
	if settings == nil || users == nil {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		recordAdminKeyRequest(c, audit, nil)
		return
	}
	item, err := settings.AuthenticateScopedAdminKey(c.Request.Context(), key, SecurityClientIP(c))
	defer func() { recordAdminKeyRequest(c, audit, item) }()
	if err != nil {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return
	}
	user, err := users.GetByID(c.Request.Context(), item.CreatedBy)
	if err != nil || user == nil || !user.IsActive() || !user.IsAdmin() {
		AbortWithError(c, 401, "INVALID_ADMIN_KEY", "Invalid admin API key")
		return
	}
	principal := &adminaccess.Principal{KeyID: item.ID, KeyName: item.Name, CreatorID: item.CreatedBy, Policy: item.Policy}
	c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), principal))
	c.Set(string(ContextKeyUser), AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})
	c.Set(string(ContextKeyUserRole), user.Role)
	c.Set(ContextKeyAuthEmail, user.Email)
	c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)
	c.Set(auditCtxKeySkip, true)
	if strings.Contains(c.FullPath(), "/settings/admin-api-key") || !adminaccess.RouteAllowed(item.Policy, c.Request.Method, c.FullPath()) {
		c.Set("admin_key_denial_reason", "permission_denied")
		AbortWithError(c, 403, "FORBIDDEN", "管理密钥未获此操作权限")
		return
	}
	c.Next()
}
func recordAdminKeyRequest(c *gin.Context, audit *service.AuditLogService, item *service.ScopedAdminKey) {
	if audit == nil {
		return
	}
	entry := &service.AuditLog{CreatedAt: time.Now().UTC(), AuthMethod: service.AuditAuthMethodAdminAPIKey, Action: "admin.api_key.request", Method: c.Request.Method, Path: c.Request.URL.Path, StatusCode: c.Writer.Status(), ClientIP: SecurityClientIP(c), UserAgent: c.Request.UserAgent()}
	if item != nil {
		entry.Extra = map[string]any{"admin_key_id": item.ID, "admin_key_name": item.Name}
		uid := item.CreatedBy
		entry.ActorUserID = &uid
	}
	if reason := c.GetString("admin_key_denial_reason"); reason != "" {
		if entry.Extra == nil {
			entry.Extra = map[string]any{}
		}
		entry.Extra["denial_reason"] = reason
	}
	audit.Record(entry)
}
