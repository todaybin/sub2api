//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：只在显式本地DSN的随机schema验证原子协调，结束删除本测试schema。
package subscriptioncontrol_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type databaseFixture struct {
	db    *sql.DB
	store *control.Store
}

func controlDatabase(t *testing.T, beforeMigration func(*sql.DB)) databaseFixture {
	t.Helper()
	dsn := os.Getenv("SUBSCRIPTION_CONTROL_TEST_DSN")
	if dsn == "" {
		t.Skip("需要显式本地SUBSCRIPTION_CONTROL_TEST_DSN")
	}
	target, err := url.Parse(dsn)
	if err != nil || (target.Scheme != "postgres" && target.Scheme != "postgresql") || (target.Hostname() != "127.0.0.1" && target.Hostname() != "localhost") {
		t.Fatal("拒绝非本地隔离数据库")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("打开隔离连接失败")
	}
	name := "sub_control_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = base.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
		base.Close()
		t.Fatal("创建隔离schema失败", err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			if e := db.Close(); e != nil {
				t.Error("关闭本测试连接失败", e)
			}
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, e := base.ExecContext(cleanup, "DROP SCHEMA "+name+" CASCADE"); e != nil {
			t.Error("回收本测试schema失败", e)
		}
		_ = base.Close()
	})
	params := target.Query()
	params.Set("search_path", name)
	target.RawQuery = params.Encode()
	db, err = sql.Open("postgres", target.String())
	if err != nil {
		t.Fatal("打开隔离连接池失败")
	}
	db.SetMaxOpenConns(6)
	_, err = db.ExecContext(ctx, `CREATE TABLE user_subscriptions(
 id BIGSERIAL PRIMARY KEY,user_id BIGINT NOT NULL,group_id BIGINT NOT NULL,
 starts_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()+interval '30 days',
 status VARCHAR(20) NOT NULL DEFAULT 'active',
 daily_window_start TIMESTAMPTZ,weekly_window_start TIMESTAMPTZ,monthly_window_start TIMESTAMPTZ,
 daily_usage_usd NUMERIC(20,10) NOT NULL DEFAULT 0,weekly_usage_usd NUMERIC(20,10) NOT NULL DEFAULT 0,monthly_usage_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),deleted_at TIMESTAMPTZ,
 assigned_by BIGINT,assigned_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),notes TEXT,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp());
 CREATE UNIQUE INDEX fixture_live_subscription ON user_subscriptions(user_id,group_id) WHERE deleted_at IS NULL;`)
	if err != nil {
		t.Fatal(err)
	}
	if beforeMigration != nil {
		beforeMigration(db)
	}
	applyControlMigration(t, db)
	applyControlMigration(t, db)
	return databaseFixture{db: db, store: control.NewStore(db)}
}

func applyControlMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	raw, err := migrations.FS.ReadFile("248_subscription_control.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, string(raw)); err != nil {
		t.Fatal("执行248迁移失败", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func grant(context.Context, *sql.Tx, int64, int64) error { return nil }
func inputCommand() control.SubscriptionControlCommand {
	return control.SubscriptionControlCommand{Protocol: control.SubscriptionControlProtocol, CommandID: uuid.NewString(), ActorUID: 9, UserID: 101, GroupID: 4, ExpectedRevision: "0", Action: "assign", Days: 30}
}

// coordinatorMutation 只构造有界数据库变更来验证协调器；完整业务规则另由服务层接入。
func coordinatorMutation(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
	var err error
	switch c.Action {
	case "assign":
		_, err = tx.ExecContext(ctx, "INSERT INTO user_subscriptions(user_id,group_id) VALUES($1,$2)", c.UserID, c.GroupID)
	case "extend":
		_, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET expires_at=expires_at+make_interval(days=>$1),updated_at=clock_timestamp() WHERE id=$2", c.Days, c.SubscriptionID)
	case "revoke":
		_, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1", c.SubscriptionID)
	case "restore":
		_, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1", c.SubscriptionID)
	case "reset_quota":
		_, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET daily_window_start=clock_timestamp(),daily_usage_usd=0,updated_at=clock_timestamp() WHERE id=$1", c.SubscriptionID)
	}
	return "", err
}

func mustApply(t *testing.T, f databaseFixture, c control.SubscriptionControlCommand) control.SubscriptionControlReceipt {
	t.Helper()
	r, err := f.store.Apply(t.Context(), "fixture-app", c, grant, coordinatorMutation)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ValidateFor(c, false); err != nil {
		t.Fatal(err)
	}
	return r
}
func readState(t *testing.T, f databaseFixture) control.SubscriptionControlState {
	t.Helper()
	s, err := f.store.State(t.Context(), "fixture-app", 101, 4, grant)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresAtomicSubscriptionCommandsAndPermanentReplay(t *testing.T) {
	f := controlDatabase(t, nil)
	c := inputCommand()
	first := mustApply(t, f, c)
	if first.Status != "succeeded" || first.State.Revision != "1" {
		t.Fatal(first)
	}
	if countRows(t, f.db, "subscription_control_cache_jobs") != 1 {
		t.Fatal("缺少缓存恢复依据")
	}
	// 已提交的命令永远返回原快照，不能用当前状态覆盖原结果。
	if _, err := f.db.Exec("UPDATE user_subscriptions SET expires_at=expires_at+interval '2 days',updated_at=clock_timestamp()"); err != nil {
		t.Fatal(err)
	}
	again, err := f.store.Apply(t.Context(), "fixture-app", c, grant, func(context.Context, *sql.Tx, control.SubscriptionControlCommand) (string, error) {
		t.Error("重放再次执行业务")
		return "", nil
	})
	if err != nil || again.State.Revision != "1" || !again.State.Subscription.ExpiresAt.Equal(first.State.Subscription.ExpiresAt) {
		t.Fatal(again, err)
	}
	digest, _ := c.Digest()
	result, err := f.store.Result(t.Context(), "fixture-app", c.CommandID, c.UserID, c.GroupID, digest, grant)
	if err != nil || result.State.Revision != "1" {
		t.Fatal(result, err)
	}
	if readState(t, f).Revision != "2" {
		t.Fatal("旧入口没有推进版本")
	}
	changed := c
	changed.Days++
	if _, err = f.store.Apply(t.Context(), "fixture-app", changed, grant, coordinatorMutation); !errors.Is(err, control.ErrCommandConflict) {
		t.Fatal("原键不同参数未冲突", err)
	}
	other, err := f.store.Result(t.Context(), "another-app", c.CommandID, c.UserID, c.GroupID, digest, grant)
	if err != nil || other.Status != "not_found" {
		t.Fatal("跨应用泄露原命令", other, err)
	}
	if _, err = f.store.Result(t.Context(), "fixture-app", c.CommandID, c.UserID, 5, digest, grant); !errors.Is(err, control.ErrCommandConflict) {
		t.Fatal("查询归属篡改未拒绝", err)
	}
	denied := errors.New("测试撤销授权")
	deny := func(context.Context, *sql.Tx, int64, int64) error { return denied }
	if _, err = f.store.Apply(t.Context(), "fixture-app", c, deny, coordinatorMutation); !errors.Is(err, denied) {
		t.Fatal("重放绕过当前授权", err)
	}
	if _, err = f.store.Result(t.Context(), "fixture-app", c.CommandID, c.UserID, c.GroupID, digest, deny); !errors.Is(err, denied) {
		t.Fatal("查询绕过当前授权", err)
	}
	for _, query := range []string{"UPDATE subscription_control_commands SET actor_uid=10", "DELETE FROM subscription_control_commands"} {
		if _, err = f.db.Exec(query); err == nil {
			t.Fatal("永久回执可以修改", query)
		}
	}
	if countRows(t, f.db, "subscription_control_commands") != 1 || countRows(t, f.db, "subscription_control_cache_jobs") != 1 {
		t.Fatal("重放生成新记录")
	}
	applyControlMigration(t, f.db)
	if readState(t, f).Revision != "2" {
		t.Fatal("重复迁移覆盖历史版本")
	}
}

func TestPostgresSubscriptionMutationReceiptAndCacheRollback(t *testing.T) {
	f := controlDatabase(t, nil)
	first := mustApply(t, f, inputCommand())
	before := readState(t, f)
	c := inputCommand()
	c.Action, c.SubscriptionID, c.ExpectedRevision, c.Days = "extend", first.State.Subscription.ID, "1", 7
	failure := errors.New("模拟事务依赖失败")
	_, err := f.store.Apply(t.Context(), "fixture-app", c, grant, func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
		_, e := coordinatorMutation(ctx, tx, c)
		if e != nil {
			return "", e
		}
		return "", failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if after := readState(t, f); after.Revision != before.Revision || !after.Subscription.ExpiresAt.Equal(before.Subscription.ExpiresAt) {
		t.Fatal("依赖失败没有共同回滚")
	}
	rejected, err := f.store.Apply(t.Context(), "fixture-app", c, grant, func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
		_, e := coordinatorMutation(ctx, tx, c)
		return "GROUP_UNAVAILABLE", e
	})
	if err != nil || rejected.Status != "rejected" || rejected.ErrorCode != "GROUP_UNAVAILABLE" {
		t.Fatal(rejected, err)
	}
	if after := readState(t, f); after.Revision != "1" || !after.Subscription.ExpiresAt.Equal(before.Subscription.ExpiresAt) {
		t.Fatal("业务拒绝未回滚业务savepoint")
	}
	if countRows(t, f.db, "subscription_control_commands") != 2 || countRows(t, f.db, "subscription_control_cache_jobs") != 1 {
		t.Fatal("拒绝产生了缓存任务或丢失永久回执")
	}
	c.CommandID = uuid.NewString()
	_, err = f.store.Apply(t.Context(), "fixture-app", c, grant, func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
		_, e := coordinatorMutation(ctx, tx, c)
		if e != nil {
			return "", e
		}
		return coordinatorMutation(ctx, tx, c)
	})
	if err == nil || readState(t, f).Revision != "1" || countRows(t, f.db, "subscription_control_commands") != 2 {
		t.Fatal("一次命令推进多个版本未被拒绝", err)
	}
	// 回执或缓存依据保存失败时，原订阅与版本也必须回滚。
	_, err = f.db.Exec(`CREATE FUNCTION reject_control_cache() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture cache rejected'; END $$;
 CREATE TRIGGER reject_control_cache BEFORE INSERT ON subscription_control_cache_jobs FOR EACH ROW EXECUTE FUNCTION reject_control_cache();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Apply(t.Context(), "fixture-app", c, grant, coordinatorMutation); err == nil {
		t.Fatal("缓存依据失败未报错")
	}
	if readState(t, f).Revision != "1" || countRows(t, f.db, "subscription_control_commands") != 2 {
		t.Fatal("缓存依据失败未共同回滚")
	}
	if _, err = f.db.Exec("DROP TRIGGER reject_control_cache ON subscription_control_cache_jobs"); err != nil {
		t.Fatal(err)
	}
	if got := mustApply(t, f, c); got.Status != "succeeded" || got.State.Revision != "2" {
		t.Fatal("原键未能安全恢复", got)
	}
}

func TestPostgresSubscriptionSlotsPreserveHistoryAndIgnoreConsumption(t *testing.T) {
	f := controlDatabase(t, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO user_subscriptions(user_id,group_id) VALUES(101,4);
  INSERT INTO user_subscriptions(user_id,group_id,deleted_at) VALUES(101,4,clock_timestamp()-interval '2 days');`); err != nil {
			t.Fatal(err)
		}
	})
	original := readState(t, f)
	if original.Revision != "1" || original.Subscription.ID != 1 {
		t.Fatal("历史初始化未优先有效订阅", original)
	}
	if _, err := f.db.Exec("UPDATE user_subscriptions SET daily_usage_usd=daily_usage_usd+1,updated_at=clock_timestamp() WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if readState(t, f).Revision != "1" {
		t.Fatal("普通消费争用控制版本")
	}
	if _, err := f.db.Exec("UPDATE user_subscriptions SET notes='fixture' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if readState(t, f).Revision != "1" {
		t.Fatal("备注更新推进了控制版本")
	}
	for _, query := range []string{
		"UPDATE user_subscriptions SET status='suspended',updated_at=clock_timestamp() WHERE id=1",
		"UPDATE user_subscriptions SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=1",
	} {
		if _, err := f.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	revoked := readState(t, f)
	if revoked.Revision != "3" || revoked.Subscription.ID != 1 || revoked.Subscription.Status != "revoked" {
		t.Fatal("撤销后选错历史订阅", revoked)
	}
	if _, err := f.db.Exec("DELETE FROM user_subscriptions"); err != nil {
		t.Fatal(err)
	}
	empty := readState(t, f)
	if empty.SlotStatus != "empty" || empty.Revision != "5" {
		t.Fatal("删除后丢失槽版本", empty)
	}
	stale := mustApply(t, f, inputCommand())
	if stale.Status != "rejected" || stale.ErrorCode != "VERSION_CONFLICT" {
		t.Fatal("删除再建发生版本ABA", stale)
	}
	fresh := inputCommand()
	fresh.ExpectedRevision = empty.Revision
	assigned := mustApply(t, f, fresh)
	if assigned.Status != "succeeded" || assigned.State.Revision != "6" {
		t.Fatal(assigned)
	}
	if _, err := f.db.Exec("UPDATE subscription_control_slots SET revision=9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE user_subscriptions SET status='suspended'"); err == nil {
		t.Fatal("最大控制版本溢出未被拒绝")
	}
	if got := readState(t, f); got.Revision != "9223372036854775807" || got.Subscription.Status != "active" {
		t.Fatal("版本溢出没有共同回滚", got)
	}
}

func TestPostgresConcurrentSubscriptionCommands(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprintf("相同原键%t", sameKey), func(t *testing.T) {
			f := controlDatabase(t, nil)
			one, two := inputCommand(), inputCommand()
			if sameKey {
				two = one
			}
			var executions atomic.Int32
			mutation := func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
				executions.Add(1)
				return coordinatorMutation(ctx, tx, c)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			results := make(chan control.SubscriptionControlReceipt, 2)
			failures := make(chan error, 2)
			for _, c := range []control.SubscriptionControlCommand{one, two} {
				wg.Add(1)
				go func(c control.SubscriptionControlCommand) {
					defer wg.Done()
					<-start
					r, e := f.store.Apply(t.Context(), "fixture-app", c, grant, mutation)
					results <- r
					failures <- e
				}(c)
			}
			close(start)
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			success, rejected := 0, 0
			for result := range results {
				if result.Status == "succeeded" {
					success++
				} else if result.Status == "rejected" && result.ErrorCode == "VERSION_CONFLICT" {
					rejected++
				} else {
					t.Fatal(result)
				}
			}
			expectedRows := 2
			if sameKey {
				expectedRows = 1
				if success != 2 {
					t.Fatal(success)
				}
			} else if success != 1 || rejected != 1 {
				t.Fatal(success, rejected)
			}
			if executions.Load() != 1 || countRows(t, f.db, "subscription_control_commands") != expectedRows || countRows(t, f.db, "subscription_control_cache_jobs") != 1 || readState(t, f).Revision != "1" {
				t.Fatal("并发命令重复修改或遗漏回执")
			}
		})
	}
}

func TestPostgresAllControlActionsAndLargeVersions(t *testing.T) {
	f := controlDatabase(t, nil)
	assigned := mustApply(t, f, inputCommand())
	id := assigned.State.Subscription.ID
	if _, err := f.db.Exec("UPDATE user_subscriptions SET status='suspended',updated_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE subscription_control_slots SET revision=9007199254740993"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"extend", "reset_quota", "revoke", "restore"} {
		state := readState(t, f)
		c := inputCommand()
		c.Action, c.SubscriptionID, c.ExpectedRevision, c.Days = action, id, state.Revision, 0
		if action == "extend" {
			c.Days = -7
		}
		if action == "reset_quota" {
			c.ResetPeriod = "all"
		}
		result := mustApply(t, f, c)
		if result.Status != "succeeded" {
			t.Fatal(action, result)
		}
		if action != "revoke" && result.State.Subscription.Status != "suspended" {
			t.Fatal("控制操作意外恢复暂停", action, result)
		}
	}
	if readState(t, f).Revision != "9007199254740997" {
		t.Fatal("大整数版本精度丢失")
	}
	var raw []byte
	if err := f.db.QueryRow("SELECT receipt FROM subscription_control_commands WHERE action='extend'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["state"].(map[string]any)["revision"] != "9007199254740994" {
		t.Fatal("回执版本被转换为浮点")
	}
}

func TestPostgresLegacyWriteAndCommandShareControlLock(t *testing.T) {
	f := controlDatabase(t, nil)
	initial := mustApply(t, f, inputCommand())
	c := inputCommand()
	c.Action, c.SubscriptionID, c.ExpectedRevision, c.Days = "extend", initial.State.Subscription.ID, "1", 1
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseMutation := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseMutation()
	var once sync.Once
	mutation := func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
		once.Do(func() { close(entered); <-release })
		return coordinatorMutation(ctx, tx, c)
	}
	result := make(chan control.SubscriptionControlReceipt, 1)
	commandError := make(chan error, 1)
	go func() { r, e := f.store.Apply(ctx, "fixture-app", c, grant, mutation); result <- r; commandError <- e }()
	select {
	case <-entered:
	case e := <-commandError:
		t.Fatal("命令尚未进入事务回调", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	legacy, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		releaseMutation()
		t.Fatal(err)
	}
	defer legacy.Rollback()
	var pid int
	if err = legacy.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		releaseMutation()
		t.Fatal(err)
	}
	legacyError := make(chan error, 1)
	go func() {
		_, e := legacy.ExecContext(ctx, "UPDATE user_subscriptions SET expires_at=expires_at+interval '2 days',updated_at=clock_timestamp() WHERE id=$1", c.SubscriptionID)
		if e == nil {
			e = legacy.Commit()
		} else {
			_ = legacy.Rollback()
		}
		legacyError <- e
	}()
	// 先观察旧SQL确实等待原槽，再释放新命令制造行/槽反向锁顺序。
	// 数据库必须回滚其中一个事务；新协调器仅重试明确已回滚的原命令。
	blocked := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
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
			break
		case <-time.After(5 * time.Millisecond):
		}
	}
	releaseMutation()
	oldErr, newErr := <-legacyError, <-commandError
	r := <-result
	if !blocked {
		t.Fatal("未观察到旧写入口参与控制锁", err)
	}
	if newErr != nil {
		t.Fatal("原命令在已回滚死锁后没有有界恢复", newErr)
	}
	if oldErr == nil {
		if r.Status != "rejected" || r.ErrorCode != "VERSION_CONFLICT" {
			t.Fatal("旧写获胜后旧版本命令仍执行", r)
		}
	} else {
		var pg *pq.Error
		if !errors.As(oldErr, &pg) || pg.Code != "40P01" || r.Status != "succeeded" {
			t.Fatal("事务冲突未保持安全边界", oldErr, r)
		}
	}
	if readState(t, f).Revision != "2" {
		t.Fatal("事务冲突重复修改控制版本")
	}
}
