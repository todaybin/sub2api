//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：以真实配置、用户分组和显式授权验证订阅边界，不用元数据前缀认领用户。
package subscriptioncontrol_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
)

const scopeApp = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const scopeSecret = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const scopeKey = "local-fixture-not-real-admin-key"

func accessFixture(t *testing.T) (databaseFixture, control.Transition) {
	t.Helper()
	f, transition := realBusinessFixture(t)
	execBusiness(t, f.db, `CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
  ALTER TABLE groups ADD COLUMN is_exclusive BOOLEAN NOT NULL DEFAULT true;
  ALTER TABLE users ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
  CREATE TABLE user_allowed_groups(user_id BIGINT NOT NULL,group_id BIGINT NOT NULL,PRIMARY KEY(user_id,group_id));
  INSERT INTO user_allowed_groups(user_id,group_id) VALUES(101,4);`)
	config, _ := json.Marshal(service.IntegrationAdminCredentials{AppID: scopeApp, Secret: scopeSecret, Enabled: true})
	execBusiness(t, f.db, "INSERT INTO settings(key,value) VALUES($1,$2),($3,$4)", service.SettingKeyIntegrationAdminCredentials, string(config), service.SettingKeyAdminAPIKey, scopeKey)
	raw, err := migrations.FS.ReadFile("249_subscription_control_grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tx, e := f.db.BeginTx(t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(t.Context(), string(raw)); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	return f, transition
}
func grantCommand() control.GrantCommand {
	return control.GrantCommand{CommandID: uuid.NewString(), AppID: scopeApp, UserID: 101, GroupID: 4, OperatorID: 501, ExpectedRevision: "0", Enabled: true, Reason: "隔离测试显式授权"}
}
func saveScope(t *testing.T, f databaseFixture, c control.GrantCommand) control.Grant {
	t.Helper()
	g, err := f.store.SaveGrant(t.Context(), c, service.SubscriptionControlActiveAppVerifier)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func signedVerifier(t *testing.T, method, path string, body []byte) control.AppVerifier {
	t.Helper()
	canonical := middleware.IntegrationCanonicalString(method, path, scopeApp, strconv.FormatInt(time.Now().Unix(), 10), "scope-fixture-nonce", body)
	mac := hmac.New(sha256.New, []byte(scopeSecret))
	mac.Write([]byte(canonical))
	verify, err := service.SubscriptionControlSignedAppVerifier(scopeKey, hex.EncodeToString(mac.Sum(nil)), canonical)
	if err != nil {
		t.Fatal(err)
	}
	return verify
}
func stateAuthorization(t *testing.T, f databaseFixture) control.Authorize {
	t.Helper()
	return f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "GET", "/api/v1/admin/subscriptions/control-v1/state", nil))
}

func TestPostgresExplicitScopeAndSignedIdentity(t *testing.T) {
	f, transition := accessFixture(t)
	// 即使外部元数据看起来属于平台，默认也没有授权。
	execBusiness(t, f.db, "UPDATE users SET external_id='workmesh:site-1' WHERE id=101")
	if _, err := f.store.State(t.Context(), scopeApp, 101, 4, stateAuthorization(t, f)); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("元数据替代了显式授权", err)
	}
	first := saveScope(t, f, grantCommand())
	if first.Revision != "1" || first.UpdatedBy != 501 || !first.Enabled {
		t.Fatal(first)
	}
	c := inputCommand()
	c.ActorUID = 9099
	raw, _ := json.Marshal(c)
	authorize := f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "PUT", "/api/v1/admin/subscriptions/control-v1/commands/"+c.CommandID, raw))
	r, err := f.store.Apply(t.Context(), scopeApp, c, authorize, transition)
	if err != nil || r.Status != "succeeded" {
		t.Fatal(r, err)
	}
	// 普通用户被停用后仍可核验已发生的原事实，新业务不能执行。
	execBusiness(t, f.db, "UPDATE users SET status='disabled' WHERE id=101")
	digest, _ := c.Digest()
	resultAuth := f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "GET", "/api/v1/admin/subscriptions/control-v1/commands/"+c.CommandID, nil))
	old, err := f.store.Result(t.Context(), scopeApp, c.CommandID, 101, 4, digest, resultAuth)
	if err != nil || old.Status != "succeeded" {
		t.Fatal("已停用目标的历史事实丢失", old, err)
	}
	next := c
	next.CommandID = uuid.NewString()
	next.Action = "extend"
	next.SubscriptionID = r.State.Subscription.ID
	next.ExpectedRevision = "1"
	next.Days = 1
	raw, _ = json.Marshal(next)
	authorize = f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "PUT", "/api/v1/admin/subscriptions/control-v1/commands/"+next.CommandID, raw))
	rejected, err := f.store.Apply(t.Context(), scopeApp, next, authorize, transition)
	if err != nil || rejected.Status != "rejected" || rejected.ErrorCode != "SUBSCRIPTION_STATE_INVALID" {
		t.Fatal("停用目标仍获业务修改", rejected, err)
	}
	// 撤销显式授权后，状态、历史结果及原键重放全部拒绝。
	revoke := grantCommand()
	revoke.ExpectedRevision = "1"
	revoke.Enabled = false
	saveScope(t, f, revoke)
	if _, err = f.store.State(t.Context(), scopeApp, 101, 4, stateAuthorization(t, f)); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("撤权后仍读取状态", err)
	}
	if _, err = f.store.Result(t.Context(), scopeApp, c.CommandID, 101, 4, digest, resultAuth); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("撤权后仍读取历史", err)
	}
	raw, _ = json.Marshal(c)
	authorize = f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "PUT", "/api/v1/admin/subscriptions/control-v1/commands/"+c.CommandID, raw))
	if _, err = f.store.Apply(t.Context(), scopeApp, c, authorize, transition); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("原键重放绕过撤权", err)
	}
	if countRows(t, f.db, "subscription_control_commands") != 2 {
		t.Fatal("撤权后留下新命令")
	}
}

func TestPostgresScopeRequiresRealResourceGrants(t *testing.T) {
	for _, name := range []string{"wrong-app", "wrong-user", "wrong-group", "public-group", "standard-group", "missing-user-group", "deleted-user", "deleted-group", "inactive-admin", "non-admin", "disabled-app", "old-api-key", "old-secret", "duplicate-config", "legacy-config", "oversized-config"} {
		t.Run(name, func(t *testing.T) {
			f, _ := accessFixture(t)
			saveScope(t, f, grantCommand())
			app, user, group, admin := scopeApp, int64(101), int64(4), int64(501)
			verify := signedVerifier(t, "GET", "/api/v1/admin/subscriptions/control-v1/state", nil)
			switch name {
			case "wrong-app":
				app = strings.Repeat("c", 32)
			case "wrong-user":
				user = 501
			case "wrong-group":
				group = 5
			case "public-group":
				execBusiness(t, f.db, "UPDATE groups SET is_exclusive=false")
			case "standard-group":
				execBusiness(t, f.db, "UPDATE groups SET subscription_type='standard'")
			case "missing-user-group":
				execBusiness(t, f.db, "DELETE FROM user_allowed_groups")
			case "deleted-user":
				execBusiness(t, f.db, "UPDATE users SET deleted_at=clock_timestamp() WHERE id=101")
			case "deleted-group":
				execBusiness(t, f.db, "UPDATE groups SET deleted_at=clock_timestamp()")
			case "inactive-admin":
				execBusiness(t, f.db, "UPDATE users SET status='disabled' WHERE id=501")
			case "non-admin":
				admin = 101
			case "disabled-app":
				execBusiness(t, f.db, "UPDATE settings SET value=$1 WHERE key=$2", `{"appid":"`+scopeApp+`","secret":"`+scopeSecret+`","enabled":false}`, service.SettingKeyIntegrationAdminCredentials)
			case "old-api-key":
				execBusiness(t, f.db, "UPDATE settings SET value='new-fixture-key' WHERE key=$1", service.SettingKeyAdminAPIKey)
			case "old-secret":
				execBusiness(t, f.db, "UPDATE settings SET value=$1 WHERE key=$2", `{"appid":"`+scopeApp+`","secret":"`+strings.Repeat("c", 64)+`","enabled":true}`, service.SettingKeyIntegrationAdminCredentials)
			case "duplicate-config":
				execBusiness(t, f.db, "UPDATE settings SET value=$1 WHERE key=$2", `{"appid":"`+scopeApp+`","appid":"`+scopeApp+`","secret":"`+scopeSecret+`","enabled":true}`, service.SettingKeyIntegrationAdminCredentials)
			case "legacy-config":
				execBusiness(t, f.db, "UPDATE settings SET value=$1 WHERE key=$2", `{"integration_id":"old","signing_secret":"old"}`, service.SettingKeyIntegrationAdminCredentials)
			case "oversized-config":
				execBusiness(t, f.db, "UPDATE settings SET value=$1 WHERE key=$2", strings.Repeat("x", 4097), service.SettingKeyIntegrationAdminCredentials)
			}
			_, err := f.store.State(t.Context(), app, user, group, f.store.GrantAuthorizer(app, admin, verify))
			if err == nil {
				t.Fatal("当前身份或实际资源授权失效仍放行")
			}
		})
	}
}

func TestPostgresScopeGrantImmutableReplayAndAtomicAudit(t *testing.T) {
	f, _ := accessFixture(t)
	c := grantCommand()
	first := saveScope(t, f, c)
	disabled := c
	disabled.CommandID = uuid.NewString()
	disabled.ExpectedRevision = "1"
	disabled.Enabled = false
	saveScope(t, f, disabled)
	replay := saveScope(t, f, c)
	firstJSON, _ := json.Marshal(first)
	replayJSON, _ := json.Marshal(replay)
	if string(replayJSON) != string(firstJSON) {
		t.Fatal("原授权结果随当前范围变化", replay, first)
	}
	var enabled bool
	if err := f.db.QueryRow("SELECT enabled FROM subscription_control_grants").Scan(&enabled); err != nil || enabled {
		t.Fatal("历史重放恢复了已撤授权", enabled, err)
	}
	conflict := c
	conflict.Reason = "另一原因"
	if _, err := f.store.SaveGrant(t.Context(), conflict, service.SubscriptionControlActiveAppVerifier); !errors.Is(err, control.ErrCommandConflict) {
		t.Fatal("同键参数冲突未拒绝", err)
	}
	stale := c
	stale.CommandID = uuid.NewString()
	if _, err := f.store.SaveGrant(t.Context(), stale, service.SubscriptionControlActiveAppVerifier); !errors.Is(err, control.ErrGrantVersionConflict) {
		t.Fatal("旧版本覆盖了授权", err)
	}
	if _, err := f.db.Exec("UPDATE subscription_control_grant_events SET reason='changed'"); err == nil {
		t.Fatal("允许改写授权审计")
	}
	if _, err := f.db.Exec("DELETE FROM subscription_control_grant_events"); err == nil {
		t.Fatal("允许删除授权审计")
	}
	execBusiness(t, f.db, `CREATE FUNCTION reject_scope_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture audit rejected'; END $$;
 CREATE TRIGGER reject_scope_event BEFORE INSERT ON subscription_control_grant_events FOR EACH ROW EXECUTE FUNCTION reject_scope_event();`)
	retry := c
	retry.CommandID = uuid.NewString()
	retry.ExpectedRevision = "2"
	if _, err := f.store.SaveGrant(t.Context(), retry, service.SubscriptionControlActiveAppVerifier); err == nil {
		t.Fatal("审计失败仍提交授权")
	}
	var revision string
	if err := f.db.QueryRow("SELECT revision::text,enabled FROM subscription_control_grants").Scan(&revision, &enabled); err != nil || revision != "2" || enabled {
		t.Fatal("审计失败未共同回滚", revision, enabled, err)
	}
	execBusiness(t, f.db, "DROP TRIGGER reject_scope_event ON subscription_control_grant_events")
	if saveScope(t, f, retry).Revision != "3" {
		t.Fatal("原键未能恢复")
	}
	execBusiness(t, f.db, "UPDATE users SET status='disabled' WHERE id=501")
	if _, err := f.store.SaveGrant(t.Context(), c, service.SubscriptionControlActiveAppVerifier); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("停用管理员仍可重放授权", err)
	}
}

func TestPostgresScopeGrantCanDisableInvalidResources(t *testing.T) {
	f, _ := accessFixture(t)
	c := grantCommand()
	saveScope(t, f, c)
	execBusiness(t, f.db, "DELETE FROM user_allowed_groups; UPDATE users SET deleted_at=clock_timestamp() WHERE id=101; UPDATE groups SET deleted_at=clock_timestamp(); DELETE FROM settings")
	c.CommandID = uuid.NewString()
	c.ExpectedRevision = "1"
	c.Enabled = false
	result := saveScope(t, f, c)
	if result.Enabled || result.Revision != "2" {
		t.Fatal("无法撤销已失效资源范围", result)
	}
}

func TestPostgresScopeGrantConcurrentCommandsAndPreciseRevision(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(map[bool]string{true: "同键", false: "异键"}[sameKey], func(t *testing.T) {
			f, _ := accessFixture(t)
			c := grantCommand()
			commands := []control.GrantCommand{c, c}
			if !sameKey {
				commands[1].CommandID = uuid.NewString()
			}
			start := make(chan struct{})
			errorsOut := make(chan error, 2)
			for _, cmd := range commands {
				go func() {
					<-start
					_, err := f.store.SaveGrant(t.Context(), cmd, service.SubscriptionControlActiveAppVerifier)
					errorsOut <- err
				}()
			}
			close(start)
			conflicts := 0
			for i := 0; i < 2; i++ {
				if err := <-errorsOut; err != nil {
					if !errors.Is(err, control.ErrGrantVersionConflict) {
						t.Fatal(err)
					}
					conflicts++
				}
			}
			want := 0
			if !sameKey {
				want = 1
			}
			if conflicts != want || countRows(t, f.db, "subscription_control_grant_events") != 1 {
				t.Fatal(conflicts)
			}
			execBusiness(t, f.db, "UPDATE subscription_control_grants SET revision=9007199254740993")
			c.CommandID = uuid.NewString()
			c.ExpectedRevision = "9007199254740993"
			c.Enabled = false
			if saveScope(t, f, c).Revision != "9007199254740994" {
				t.Fatal("授权版本发生精度丢失")
			}
		})
	}
}

func TestPostgresScopeReadHoldsGrantUntilCommit(t *testing.T) {
	f, transition := accessFixture(t)
	saveScope(t, f, grantCommand())
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	c := inputCommand()
	raw, _ := json.Marshal(c)
	actual := f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "PUT", "/api/v1/admin/subscriptions/control-v1/commands/"+c.CommandID, raw))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	commandDone := make(chan error, 1)
	go func() {
		_, err := f.store.Apply(ctx, scopeApp, c, func(ctx context.Context, tx *sql.Tx, user, group int64) error {
			if err := actual(ctx, tx, user, group); err != nil {
				return err
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, transition)
		commandDone <- err
	}()
	select {
	case <-entered:
	case err := <-commandDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	revocation, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer revocation.Rollback()
	var pid int
	if err = revocation.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	revokeDone := make(chan error, 1)
	go func() {
		_, err := revocation.ExecContext(ctx, "UPDATE subscription_control_grants SET enabled=false,revision=revision+1")
		if err == nil {
			err = revocation.Commit()
		} else {
			revocation.Rollback()
		}
		revokeDone <- err
	}()
	blocked := false
	for end := time.Now().Add(time.Second); time.Now().Before(end); {
		var n int
		if err = f.db.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1))", pid).Scan(&n); err != nil {
			break
		}
		if n > 0 {
			blocked = true
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Millisecond):
		}
	}
	unblock()
	if err = <-commandDone; err != nil {
		t.Fatal(err)
	}
	if err = <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("未持有授权共享锁", err)
	}
	if _, err = f.store.State(t.Context(), scopeApp, 101, 4, stateAuthorization(t, f)); !errors.Is(err, control.ErrAccessDenied) {
		t.Fatal("撤权提交后仍放行", err)
	}
}
