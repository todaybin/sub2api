package admin

import (
	"context"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type idempotencyStoreUnavailableMode int

const (
	idempotencyStoreUnavailableFailClose idempotencyStoreUnavailableMode = iota
	idempotencyStoreUnavailableFailOpen
)

func executeAdminIdempotent(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithTimeout(c, scope, payload, ttl, 0, execute)
}

func executeAdminIdempotentWithTimeout(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	coordinator := service.DefaultIdempotencyCoordinator()
	// Integrations are a new contract: require keys even when legacy clients
	// remain in observe-only mode, and never execute without a coordinator.
	if c.GetString(middleware2.IntegrationAppIDContextKey) != "" {
		key, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, infraerrors.BadRequest("IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required")
		}
		if coordinator == nil {
			return nil, service.ErrIdempotencyStoreUnavail
		}
	}
	if coordinator == nil {
		ctx := c.Request.Context()
		if executionTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), executionTimeout)
			defer cancel()
		}
		data, err := execute(ctx)
		if err != nil {
			return nil, err
		}
		return &service.IdempotencyExecuteResult{Data: data}, nil
	}

	return coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
		Scope:             scope,
		ActorScope:        adminActorScope(c),
		Method:            c.Request.Method,
		Route:             c.FullPath(),
		IdempotencyKey:    c.GetHeader("Idempotency-Key"),
		Payload:           payload,
		RequireKey:        true,
		SensitiveResponse: scope == "admin.users.token.issue" || (c.GetString(middleware2.IntegrationAppIDContextKey) != "" && (scope == "admin.users.api-keys.create" || scope == "admin.users.provision")),
		TTL:               ttl,
		ExecutionTimeout:  executionTimeout,
	}, execute)
}

func adminActorScope(c *gin.Context) string {
	if appid := c.GetString(middleware2.IntegrationAppIDContextKey); appid != "" {
		return "integration:" + appid
	}
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	return actorScope
}

func executeAdminIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, 0, idempotencyStoreUnavailableFailClose, execute)
}

func executeAdminIdempotentJSONWithTimeout(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, executionTimeout, idempotencyStoreUnavailableFailClose, execute)
}

func executeAdminIdempotentJSONFailOpenOnStoreUnavailable(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, 0, idempotencyStoreUnavailableFailOpen, execute)
}

func executeAdminIdempotentJSONWithMode(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	mode idempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
) {
	result, err := executeAdminIdempotentWithTimeout(c, scope, payload, ttl, executionTimeout, execute)
	if err != nil {
		failOpen := mode == idempotencyStoreUnavailableFailOpen && c.GetString(middleware2.IntegrationAppIDContextKey) == ""
		if infraerrors.Code(err) == infraerrors.Code(service.ErrIdempotencyStoreUnavail) {
			strategy := "fail_close"
			if failOpen {
				strategy = "fail_open"
			}
			service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_"+strategy)
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=%s", c.Request.Method, c.FullPath(), scope, strategy)
			if failOpen {
				data, fallbackErr := execute(c.Request.Context())
				if fallbackErr != nil {
					response.ErrorFrom(c, fallbackErr)
					return
				}
				c.Header("X-Idempotency-Degraded", "store-unavailable")
				response.Success(c, data)
				return
			}
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
