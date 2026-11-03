// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：验证新订阅协议与旧订阅详情路由共存，并确实分派到受保护的新处理器。
package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
)

func TestSubscriptionControlRoutesRequireIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Subscription: admin.NewSubscriptionControlHandler(nil, nil)}}
	registerSubscriptionRoutes(router.Group("/api/v1/admin"), h)
	tests := []struct{ method, path string }{
		{"GET", "/subscriptions/control-v1/state?user_id=101&group_id=4"},
		{"PUT", "/subscriptions/control-v1/commands/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"GET", "/subscriptions/control-v1/commands/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/admin"+tc.path, nil))
		if w.Code != 403 {
			t.Fatal("协议未进入独立集成认证门禁", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
