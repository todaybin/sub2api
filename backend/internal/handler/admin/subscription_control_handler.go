// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：解析签名订阅控制协议并返回原子回执，普通JWT和受限管理密钥不能调用。
package admin

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// NewSubscriptionControlHandler 显式装配原订阅服务和持久协调器，不启动进程或执行订阅。
func NewSubscriptionControlHandler(svc *service.SubscriptionService, store *control.Store) *SubscriptionHandler {
	return &SubscriptionHandler{subscriptionService: svc, controlStore: store}
}

type subscriptionControlRequest struct {
	app       string
	admin     int64
	body      []byte
	authorize control.Authorize
}

func (h *SubscriptionHandler) controlRequest(c *gin.Context) (subscriptionControlRequest, bool) {
	var r subscriptionControlRequest
	if !isIntegrationRequest(c) {
		subscriptionControlError(c, control.ErrAccessDenied)
		return r, false
	}
	r.app = c.GetString(middleware.IntegrationAppIDContextKey)
	r.admin = getAdminIDFromContext(c)
	if r.app == "" || r.admin < 1 {
		subscriptionControlError(c, control.ErrAccessDenied)
		return r, false
	}
	if h.controlStore == nil || h.subscriptionService == nil {
		subscriptionControlError(c, control.ErrStoreUnavailable)
		return r, false
	}
	var err error
	r.body, err = io.ReadAll(io.LimitReader(c.Request.Body, 4097))
	if err != nil || len(r.body) > 4096 || c.Request.Method == "GET" && len(r.body) != 0 {
		subscriptionControlError(c, control.ErrSubscriptionControlInput)
		return r, false
	}
	canonical := middleware.IntegrationCanonicalString(c.Request.Method, c.Request.URL.Path, r.app, strings.TrimSpace(c.GetHeader("X-Timestamp")), strings.TrimSpace(c.GetHeader("X-Nonce")), r.body)
	verifier, err := service.SubscriptionControlSignedAppVerifier(c.GetHeader("x-api-key"), c.GetHeader("X-Signature"), canonical)
	if err != nil {
		subscriptionControlError(c, err)
		return r, false
	}
	r.authorize = h.controlStore.GrantAuthorizer(r.app, r.admin, verifier)
	return r, true
}

// SubscriptionControlState 只返回获显式授权的控制槽，不创建订阅或命令。
func (h *SubscriptionHandler) SubscriptionControlState(c *gin.Context) {
	r, ok := h.controlRequest(c)
	if !ok {
		return
	}
	values, user, group, err := controlQuery(c.Request.URL.RawQuery, false)
	if err != nil || len(values) != 2 {
		subscriptionControlError(c, control.ErrSubscriptionControlInput)
		return
	}
	state, err := h.controlStore.State(c.Request.Context(), r.app, user, group, r.authorize)
	if err != nil {
		subscriptionControlError(c, err)
		return
	}
	response.Success(c, state)
}

// SubscriptionControlApply 按原UUID、精确版本及规范正文提交单个原子命令。
func (h *SubscriptionHandler) SubscriptionControlApply(c *gin.Context) {
	r, ok := h.controlRequest(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		subscriptionControlError(c, control.ErrSubscriptionControlInput)
		return
	}
	command, err := control.DecodeCommand(bytes.NewReader(r.body), c.Param("commandId"), c.GetHeader("Idempotency-Key"))
	if err != nil {
		subscriptionControlError(c, err)
		return
	}
	receipt, err := h.controlStore.Apply(c.Request.Context(), r.app, command, r.authorize, h.subscriptionService.SubscriptionControlTransition(r.admin))
	if err != nil {
		subscriptionControlError(c, err)
		return
	}
	response.Success(c, receipt)
}

// SubscriptionControlResult 只读原应用原命令的永久事实，不重发或执行。
func (h *SubscriptionHandler) SubscriptionControlResult(c *gin.Context) {
	r, ok := h.controlRequest(c)
	if !ok {
		return
	}
	values, user, group, err := controlQuery(c.Request.URL.RawQuery, true)
	if err != nil || len(values) != 3 {
		subscriptionControlError(c, control.ErrSubscriptionControlInput)
		return
	}
	receipt, err := h.controlStore.Result(c.Request.Context(), r.app, c.Param("commandId"), user, group, values.Get("request_digest"), r.authorize)
	if err != nil {
		subscriptionControlError(c, err)
		return
	}
	response.Success(c, receipt)
}

func controlQuery(raw string, result bool) (url.Values, int64, int64, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, 0, 0, control.ErrSubscriptionControlInput
	}
	for key, items := range values {
		if len(items) != 1 || key != "user_id" && key != "group_id" && !(result && key == "request_digest") {
			return nil, 0, 0, control.ErrSubscriptionControlInput
		}
	}
	user, err := strconv.ParseInt(values.Get("user_id"), 10, 64)
	if err != nil || user < 1 || strconv.FormatInt(user, 10) != values.Get("user_id") {
		return nil, 0, 0, control.ErrSubscriptionControlInput
	}
	group, err := strconv.ParseInt(values.Get("group_id"), 10, 64)
	if err != nil || group < 1 || strconv.FormatInt(group, 10) != values.Get("group_id") {
		return nil, 0, 0, control.ErrSubscriptionControlInput
	}
	return values, user, group, nil
}

func subscriptionControlError(c *gin.Context, err error) {
	status, code := http.StatusServiceUnavailable, "SUBSCRIPTION_CONTROL_UNAVAILABLE"
	switch {
	case errors.Is(err, control.ErrSubscriptionControlInput):
		status, code = http.StatusBadRequest, "MODEL_SUBSCRIPTION_COMMAND_INVALID"
	case errors.Is(err, control.ErrAccessDenied):
		status, code = http.StatusForbidden, "SUBSCRIPTION_CONTROL_ACCESS_DENIED"
	case errors.Is(err, control.ErrCommandConflict):
		status, code = http.StatusConflict, "SUBSCRIPTION_CONTROL_COMMAND_CONFLICT"
	}
	response.ErrorWithDetails(c, status, "订阅控制请求未完成", code, nil)
}
