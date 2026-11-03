//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：以两个独立Go进程和回环TLS验证公开订阅契约，不跨仓导入Gateway内部实现。
package subscriptioncontrol_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresSubscriptionGatewayEndToEnd(t *testing.T) {
	root := os.Getenv("WORKMESH_SUBSCRIPTION_E2E_ROOT")
	if root == "" {
		t.Skip("未明确提供Gateway工作区：跨进程订阅联调未运行")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal("Gateway工作区无效")
	}
	manifest, err := os.ReadFile(filepath.Join(root, "apps", "gateway", "go.mod"))
	if err != nil || !bytes.Contains(manifest, []byte("module workmesh-saas-module")) {
		t.Fatal("未找到明确的Gateway模块")
	}
	f, _ := accessFixture(t)
	saveScope(t, f, grantCommand())
	execBusiness(t, f.db, "INSERT INTO users(id,role,status) VALUES(201,'user','active'); INSERT INTO user_allowed_groups(user_id,group_id) VALUES(201,4)")
	enterprise := grantCommand()
	enterprise.UserID = 201
	saveScope(t, f, enterprise)
	// 验证JSON/数据库全过程保持超过JavaScript安全整数的精确控制版本。
	execBusiness(t, f.db, "INSERT INTO subscription_control_slots(user_id,group_id,revision) VALUES(101,4,9007199254740993)")
	router := controlHTTPFixture(t, f, nil)
	var puts, results, states atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			puts.Add(1)
		} else if strings.HasSuffix(r.URL.Path, "/state") {
			states.Add(1)
		} else {
			results.Add(1)
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "-C", filepath.Join(root, "apps", "gateway"), "test", "./test/workmeshadmin", "-run", "^TestPostgresSubscriptionActualPeerEndToEnd$", "-count=1", "-timeout=85s", "-v")
	cmd.Dir = root
	// 子进程只获得公开地址/临时证书与测试身份，不传递上游数据库连接。
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SUBSCRIPTION_CONTROL_TEST_DSN=") && !strings.HasPrefix(entry, "WORKMESH_SUBSCRIPTION_E2E_") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env,
		"WORKMESH_SUBSCRIPTION_E2E_URL="+server.URL,
		"WORKMESH_SUBSCRIPTION_E2E_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw),
		"WORKMESH_ADMIN_TEST_CONFIG="+filepath.Join(root, "apps", "gateway", "config"),
		"WORKMESH_RESOURCE_ROOT="+filepath.Join(root, "apps", "gateway", "resources"),
		"GOWORK=off",
	)
	output, err := cmd.CombinedOutput()
	if bytes.Contains(output, []byte(scopeSecret)) || bytes.Contains(output, []byte(scopeKey)) {
		t.Fatal("子进程输出泄露测试凭据")
	}
	t.Logf("Gateway限定进程输出：\n%s", output)
	if err != nil {
		t.Fatal("Gateway实际客户端端到端验证失败", err)
	}
	var commands, success, rejected, jobs, personal, enterpriseCount int
	if err = f.db.QueryRow("SELECT count(*),count(*) FILTER (WHERE status='succeeded'),count(*) FILTER (WHERE status='rejected') FROM subscription_control_commands").Scan(&commands, &success, &rejected); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM subscription_control_cache_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FILTER (WHERE user_id=101),count(*) FILTER (WHERE user_id=201) FROM user_subscriptions WHERE deleted_at IS NULL").Scan(&personal, &enterpriseCount); err != nil {
		t.Fatal(err)
	}
	var revision string
	if err = f.db.QueryRow("SELECT revision::text FROM subscription_control_slots WHERE user_id=101 AND group_id=4").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if commands != 9 || success != 8 || rejected != 1 || jobs != 8 || personal != 1 || enterpriseCount != 1 || revision != "9007199254741000" {
		t.Fatal("两端永久事实或精确版本不一致", commands, success, rejected, jobs, personal, enterpriseCount, revision)
	}
	var wrongActors int
	if err = f.db.QueryRow("SELECT count(*) FROM subscription_control_commands WHERE actor_uid<>2").Scan(&wrongActors); err != nil || wrongActors != 0 {
		t.Fatal("上游审计未保留真实平台操作者", wrongActors, err)
	}
	var wrongAssignee int
	if err = f.db.QueryRow("SELECT count(*) FROM user_subscriptions WHERE assigned_by<>501").Scan(&wrongAssignee); err != nil || wrongAssignee != 0 {
		t.Fatal("平台操作者冒充本地管理员", wrongAssignee, err)
	}
	if puts.Load() != 11 || results.Load() < 1 || states.Load() < 8 {
		t.Fatal("重放/恢复请求次数异常", puts.Load(), results.Load(), states.Load())
	}
	t.Logf("双端事实已核对：commands=%d succeeded=%d rejected=%d cache_jobs=%d revision=%s PUT=%d result_GET=%d state_GET=%d", commands, success, rejected, jobs, revision, puts.Load(), results.Load(), states.Load())
}
