//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：用真实签名网关、权限中间件、数据库授权与业务动作验证订阅HTTP；Redis使用隔离模拟器。
package subscriptioncontrol_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type controlHTTPSettings struct {
	service.SettingRepository
	db *sql.DB
}

func (r *controlHTTPSettings) GetValue(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrSettingNotFound
	}
	return value, err
}

type controlHTTPUsers struct {
	service.UserRepository
	db *sql.DB
}

func (r *controlHTTPUsers) GetFirstAdmin(ctx context.Context) (*service.User, error) {
	u := &service.User{}
	err := r.db.QueryRowContext(ctx, "SELECT id,role,status FROM users WHERE role='admin' AND deleted_at IS NULL ORDER BY id LIMIT 1").Scan(&u.ID, &u.Role, &u.Status)
	return u, err
}

const controlInternal = "/api/v1/admin/subscriptions/control-v1"
const controlExternal = "/api/v1/integrations/admin/subscriptions/control-v1"

func controlHTTPFixture(t *testing.T, f databaseFixture, before gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cache := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	settings := service.NewSettingService(&controlHTTPSettings{db: f.db}, &config.Config{})
	users := service.NewUserService(&controlHTTPUsers{db: f.db}, nil, nil, nil)
	ack, _ := json.Marshal(service.AdminComplianceAcknowledgement{Version: service.AdminComplianceVersion, AdminUserID: 501})
	execBusiness(t, f.db, "INSERT INTO settings(key,value) VALUES('admin_compliance_acknowledgement:501',$1)", string(ack))
	h := admin.NewSubscriptionControlHandler(&service.SubscriptionService{}, f.store)
	r := gin.New()
	group := r.Group("/api/v1/admin")
	group.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(nil, users, settings, nil)))
	group.Use(middleware.NewPanelRateLimiter(client, settings).Global())
	group.Use(middleware.AdminComplianceGuard(settings))
	group.Use((&admin.AccountHandler{}).ScopedAdminKeyAccounts)
	if before != nil {
		group.Use(before)
	}
	group.GET("/subscriptions/control-v1/state", h.SubscriptionControlState)
	group.PUT("/subscriptions/control-v1/commands/:commandId", h.SubscriptionControlApply)
	group.GET("/subscriptions/control-v1/commands/:commandId", h.SubscriptionControlResult)
	r.Any("/api/v1/integrations/admin/*path", middleware.NewIntegrationAdminGateway(r, settings, users, client))
	// 直接构造已认证JWT/受限密钥身份，验证handler不能因管理员身份而绕过集成域。
	for _, method := range []string{"jwt", "scoped_admin_key"} {
		r.GET("/fixture/"+method, func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 501})
			c.Set("auth_method", method)
			h.SubscriptionControlState(c)
		})
	}
	return r
}
func signedControlRequest(method, suffix string, body []byte) *http.Request {
	req := httptest.NewRequest(method, controlExternal+suffix, bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	req.Header.Set("x-api-key", scopeKey)
	req.Header.Set("X-App-Id", scopeApp)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	target := strings.Split(controlInternal+suffix, "?")[0]
	canonical := middleware.IntegrationCanonicalString(method, target, scopeApp, timestamp, nonce, body)
	mac := hmac.New(sha256.New, []byte(scopeSecret))
	mac.Write([]byte(canonical))
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	return req
}
func controlHTTPCall(t *testing.T, r *gin.Engine, req *http.Request, status int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("HTTP状态错误 got=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
	if strings.Contains(w.Body.String(), scopeSecret) || strings.Contains(w.Body.String(), scopeKey) {
		t.Fatal("响应泄露凭据")
	}
	return w
}
func httpState(t *testing.T, r *gin.Engine) control.SubscriptionControlState {
	t.Helper()
	w := controlHTTPCall(t, r, signedControlRequest("GET", "/state?user_id=101&group_id=4", nil), 200)
	var envelope struct {
		Code int
		Data control.SubscriptionControlState
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 || envelope.Data.Protocol != control.SubscriptionControlProtocol {
		t.Fatal("对端信封或协议错误", err)
	}
	return envelope.Data
}
func httpApply(t *testing.T, r *gin.Engine, c control.SubscriptionControlCommand, status int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(c)
	req := signedControlRequest("PUT", "/commands/"+c.CommandID, body)
	req.Header.Set("Idempotency-Key", c.CommandID)
	return controlHTTPCall(t, r, req, status)
}
func httpReceipt(t *testing.T, w *httptest.ResponseRecorder) control.SubscriptionControlReceipt {
	t.Helper()
	var envelope struct {
		Code int
		Data control.SubscriptionControlReceipt
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 {
		t.Fatal(err, w.Body.String())
	}
	return envelope.Data
}

func TestPostgresSubscriptionHTTPGatewayBusinessAndReplay(t *testing.T) {
	f, _ := accessFixture(t)
	r := controlHTTPFixture(t, f, nil)
	controlHTTPCall(t, r, signedControlRequest("GET", "/state?user_id=101&group_id=4", nil), 403)
	saveScope(t, f, grantCommand())
	state := httpState(t, r)
	if state.Revision != "0" {
		t.Fatal(state)
	}
	var original control.SubscriptionControlCommand
	var first control.SubscriptionControlReceipt
	for _, action := range []string{"assign", "extend", "reset_quota", "revoke", "restore"} {
		c := inputCommand()
		c.Action = action
		c.ExpectedRevision = state.Revision
		c.Days = 0
		if action == "assign" {
			c.Days = 30
		} else {
			c.SubscriptionID = state.Subscription.ID
		}
		if action == "extend" {
			c.Days = -1
		}
		if action == "reset_quota" {
			c.ResetPeriod = "all"
		}
		got := httpReceipt(t, httpApply(t, r, c, 200))
		if err := got.ValidateFor(c, false); err != nil || got.Status != "succeeded" {
			t.Fatal(action, got, err)
		}
		if action == "assign" {
			original, first = c, got
		}
		state = httpState(t, r)
		if state.Revision != got.State.Revision {
			t.Fatal("读取未返回已提交事实", state, got)
		}
	}
	// 原键重放返回原固定快照，不能变成最新状态或再次执行。
	replay := httpReceipt(t, httpApply(t, r, original, 200))
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatal("HTTP重放改变原回执")
	}
	digest, _ := original.Digest()
	suffix := "/commands/" + original.CommandID + "?user_id=101&group_id=4&request_digest=" + digest
	result := httpReceipt(t, controlHTTPCall(t, r, signedControlRequest("GET", suffix, nil), 200))
	b, _ = json.Marshal(result)
	if string(a) != string(b) {
		t.Fatal("结果查询不是原事实")
	}
	altered := original
	altered.Days++
	httpApply(t, r, altered, 409)
	req := signedControlRequest("GET", "/state?user_id=101&group_id=4", nil)
	duplicate := req.Clone(t.Context())
	controlHTTPCall(t, r, req, 200)
	controlHTTPCall(t, r, duplicate, 409)
	grant := grantCommand()
	grant.ExpectedRevision = "1"
	grant.Enabled = false
	saveScope(t, f, grant)
	controlHTTPCall(t, r, signedControlRequest("GET", suffix, nil), 403)
	httpApply(t, r, original, 403)
	var commands, jobs, assignedBy int
	if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_cache_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT assigned_by FROM user_subscriptions").Scan(&assignedBy); err != nil {
		t.Fatal(err)
	}
	if commands != 5 || jobs != 5 || assignedBy != 501 {
		t.Fatal("重放重复修改或操作者错误", commands, jobs, assignedBy)
	}
}

func TestPostgresSubscriptionHTTPInputAndIdentityIsolation(t *testing.T) {
	f, _ := accessFixture(t)
	saveScope(t, f, grantCommand())
	r := controlHTTPFixture(t, f, nil)
	for _, query := range []string{
		"?user_id=101&group_id=4&extra=x", "?user_id=101&user_id=101&group_id=4",
		"?user_id=0101&group_id=4", "?user_id=101&group_id=+4", "?user_id=101", "?user_id=101&group_id=4;bad=x",
	} {
		controlHTTPCall(t, r, signedControlRequest("GET", "/state"+query, nil), 400)
	}
	controlHTTPCall(t, r, signedControlRequest("GET", "/state?user_id=102&group_id=4", nil), 403)
	controlHTTPCall(t, r, signedControlRequest("GET", "/state?user_id=101&group_id=4", []byte("{}")), 400)
	for _, method := range []string{"jwt", "scoped_admin_key"} {
		controlHTTPCall(t, r, httptest.NewRequest("GET", "/fixture/"+method, nil), 403)
	}
	direct := signedControlRequest("GET", "/state?user_id=101&group_id=4", nil)
	direct.URL.Path = controlInternal + "/state"
	controlHTTPCall(t, r, direct, 401)
	bad := signedControlRequest("GET", "/state?user_id=101&group_id=4", nil)
	bad.Header.Set("X-Signature", strings.Repeat("0", 64))
	controlHTTPCall(t, r, bad, 401)
	c := inputCommand()
	body, _ := json.Marshal(c)
	for _, test := range []struct {
		suffix, key string
		body        []byte
	}{
		{"/commands/" + c.CommandID, "", body},
		{"/commands/" + c.CommandID, uuid.NewString(), body},
		{"/commands/" + c.CommandID + "?user_id=101", c.CommandID, body},
		{"/commands/" + c.CommandID, c.CommandID, append(body, []byte("{}")...)},
		{"/commands/" + c.CommandID, c.CommandID, []byte(strings.Repeat("x", 4097))},
	} {
		req := signedControlRequest("PUT", test.suffix, test.body)
		req.Header.Set("Idempotency-Key", test.key)
		controlHTTPCall(t, r, req, 400)
	}
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_commands").Scan(&count); err != nil || count != 0 {
		t.Fatal("拒绝路径发生业务写入", count, err)
	}
}

func TestPostgresSubscriptionHTTPRechecksAuthorityAfterGateway(t *testing.T) {
	for _, change := range []string{"secret", "key", "grant", "admin", "compliance"} {
		t.Run(change, func(t *testing.T) {
			f, _ := accessFixture(t)
			saveScope(t, f, grantCommand())
			armed := false
			r := controlHTTPFixture(t, f, func(c *gin.Context) {
				if !armed {
					return
				}
				armed = false
				switch change {
				case "secret":
					execBusiness(t, f.db, "UPDATE settings SET value=replace(value,$1,$2) WHERE key=$3", scopeSecret, strings.Repeat("c", 64), service.SettingKeyIntegrationAdminCredentials)
				case "key":
					execBusiness(t, f.db, "UPDATE settings SET value='rotated-local-key' WHERE key=$1", service.SettingKeyAdminAPIKey)
				case "grant":
					g := grantCommand()
					g.Enabled = false
					g.ExpectedRevision = "1"
					saveScope(t, f, g)
				case "admin":
					execBusiness(t, f.db, "UPDATE users SET status='disabled' WHERE id=501")
				}
			})
			if change == "compliance" {
				execBusiness(t, f.db, "DELETE FROM settings WHERE key='admin_compliance_acknowledgement:501'")
				httpApply(t, r, inputCommand(), 423)
			} else {
				armed = true
				httpApply(t, r, inputCommand(), 403)
			}
			var count int
			if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_commands").Scan(&count); err != nil || count != 0 {
				t.Fatal("身份变化后仍执行", count, err)
			}
		})
	}
}
