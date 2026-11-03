//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：通过真实服务动作验证订阅事务、既有期限规则和额度窗口，不把测试回调当业务实现。
package subscriptioncontrol_test

import (
	"database/sql"
	"testing"
	"time"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func realBusinessFixture(t *testing.T) (databaseFixture, control.Transition) {
	t.Helper()
	f := controlDatabase(t, nil)
	_, err := f.db.Exec(`CREATE TABLE users(id BIGINT PRIMARY KEY,role TEXT NOT NULL,status TEXT NOT NULL,deleted_at TIMESTAMPTZ);
 CREATE TABLE groups(id BIGINT PRIMARY KEY,subscription_type TEXT NOT NULL,status TEXT NOT NULL,deleted_at TIMESTAMPTZ);
 INSERT INTO users(id,role,status) VALUES(101,'user','active'),(501,'admin','active');
 INSERT INTO groups(id,subscription_type,status) VALUES(4,'subscription','active');
 ALTER TABLE user_subscriptions ADD FOREIGN KEY(user_id) REFERENCES users(id);
 ALTER TABLE user_subscriptions ADD FOREIGN KEY(group_id) REFERENCES groups(id);
 ALTER TABLE user_subscriptions ADD FOREIGN KEY(assigned_by) REFERENCES users(id);`)
	if err != nil {
		t.Fatal(err)
	}
	return f, (&service.SubscriptionService{}).SubscriptionControlTransition(501)
}

func applyRealAction(t *testing.T, f databaseFixture, transition control.Transition, action string, days int, period string) control.SubscriptionControlReceipt {
	t.Helper()
	state := readState(t, f)
	c := inputCommand()
	c.CommandID = uuid.NewString()
	c.ActorUID = 9099
	c.ExpectedRevision, c.Action, c.Days, c.ResetPeriod = state.Revision, action, days, period
	if action != "assign" {
		c.SubscriptionID = state.Subscription.ID
	}
	r, err := f.store.Apply(t.Context(), "fixture-app", c, grant, transition)
	if err != nil {
		t.Fatal(action, err)
	}
	if err = r.ValidateFor(c, false); err != nil {
		t.Fatal(action, err)
	}
	return r
}

func execBusiness(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRealSubscriptionBusinessActions(t *testing.T) {
	f, transition := realBusinessFixture(t)
	before := time.Now()
	assigned := applyRealAction(t, f, transition, "assign", 30, "")
	if assigned.Status != "succeeded" || assigned.State.Revision != "1" {
		t.Fatal(assigned)
	}
	sub := assigned.State.Subscription
	if sub.StartsAt.Before(before) || sub.ExpiresAt.Sub(sub.StartsAt) < 29*24*time.Hour {
		t.Fatal("分配未使用真实期限", sub)
	}
	var assignedBy int64
	var daily, weekly, monthly sql.NullTime
	if err := f.db.QueryRow("SELECT assigned_by,daily_window_start,weekly_window_start,monthly_window_start FROM user_subscriptions").Scan(&assignedBy, &daily, &weekly, &monthly); err != nil {
		t.Fatal(err)
	}
	if assignedBy != 501 || daily.Valid || weekly.Valid || monthly.Valid {
		t.Fatal("平台actor被当作本地管理员，或提前激活窗口", assignedBy)
	}
	originalEnd := sub.ExpiresAt
	extended := applyRealAction(t, f, transition, "extend", 7, "")
	if extended.Status != "succeeded" || !extended.State.Subscription.ExpiresAt.Equal(originalEnd.In(timezone.Location()).AddDate(0, 0, 7)) {
		t.Fatal(extended)
	}
	shortened := applyRealAction(t, f, transition, "extend", -7, "")
	if shortened.Status != "succeeded" || !shortened.State.Subscription.ExpiresAt.Equal(originalEnd) {
		t.Fatal(shortened)
	}

	for _, period := range []string{"daily", "weekly", "monthly", "all"} {
		execBusiness(t, f.db, "UPDATE user_subscriptions SET daily_usage_usd=11,weekly_usage_usd=22,monthly_usage_usd=33,daily_window_start=$1,weekly_window_start=$1,monthly_window_start=$1", time.Now().Add(-48*time.Hour))
		resetBefore := time.Now()
		r := applyRealAction(t, f, transition, "reset_quota", 0, period)
		if r.Status != "succeeded" || !r.State.Subscription.ExpiresAt.Equal(originalEnd) {
			t.Fatal(period, r)
		}
		var du, wu, mu string
		var d, w, m time.Time
		if err := f.db.QueryRow("SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_subscriptions").Scan(&du, &wu, &mu, &d, &w, &m); err != nil {
			t.Fatal(err)
		}
		wantD, wantW, wantM := "11.0000000000", "22.0000000000", "33.0000000000"
		if period == "daily" || period == "all" {
			wantD = "0.0000000000"
			if !d.Equal(timezone.StartOfDay(resetBefore)) {
				t.Fatal("日窗口未对齐本地零点", d)
			}
		}
		if period == "weekly" || period == "all" {
			wantW = "0.0000000000"
			if w.Before(resetBefore) {
				t.Fatal("周窗口未锚定本次重置", w)
			}
		}
		if period == "monthly" || period == "all" {
			wantM = "0.0000000000"
			if m.Before(resetBefore) {
				t.Fatal("月窗口未锚定本次重置", m)
			}
		}
		if du != wantD || wu != wantW || mu != wantM {
			t.Fatal("重置了非目标用量", period, du, wu, mu)
		}
		if period == "daily" && (!w.Before(resetBefore) || !m.Before(resetBefore)) {
			t.Fatal("日重置覆盖其他窗口")
		}
		if period == "weekly" && (!d.Before(resetBefore) || !m.Before(resetBefore)) {
			t.Fatal("周重置覆盖其他窗口")
		}
		if period == "monthly" && (!d.Before(resetBefore) || !w.Before(resetBefore)) {
			t.Fatal("月重置覆盖其他窗口")
		}
	}
	execBusiness(t, f.db, "UPDATE user_subscriptions SET status='suspended'")
	for _, action := range []string{"extend", "reset_quota", "revoke", "restore"} {
		days, period := 0, ""
		if action == "extend" {
			days = 1
		}
		if action == "reset_quota" {
			period = "all"
		}
		r := applyRealAction(t, f, transition, action, days, period)
		expected := "suspended"
		if action == "revoke" {
			expected = "revoked"
		}
		if r.Status != "succeeded" || r.State.Subscription.Status != expected {
			t.Fatal("意外解除暂停", action, r)
		}
	}
	var commands, cacheJobs int
	if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_commands WHERE status='succeeded'").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM subscription_control_cache_jobs").Scan(&cacheJobs); err != nil {
		t.Fatal(err)
	}
	if commands != 11 || cacheJobs != commands {
		t.Fatal("真实动作和缓存恢复依据数量不一致", commands, cacheJobs)
	}
}

func TestPostgresRealSubscriptionExpiryAndRestore(t *testing.T) {
	for _, status := range []string{"active", "expired", "suspended"} {
		t.Run(status, func(t *testing.T) {
			f, transition := realBusinessFixture(t)
			applyRealAction(t, f, transition, "assign", 30, "")
			oldEnd := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
			oldStart := oldEnd.AddDate(0, 0, -30)
			execBusiness(t, f.db, "UPDATE user_subscriptions SET starts_at=$1,expires_at=$2,status=$3,daily_usage_usd=17", oldStart, oldEnd, status)
			revision := readState(t, f).Revision
			bad := applyRealAction(t, f, transition, "extend", -1, "")
			if bad.Status != "rejected" || bad.ErrorCode != "SUBSCRIPTION_STATE_INVALID" || readState(t, f).Revision != revision {
				t.Fatal("到期缩短未被原子拒绝", bad)
			}
			revoked := applyRealAction(t, f, transition, "revoke", 0, "")
			if revoked.Status != "succeeded" || !revoked.State.Subscription.ExpiresAt.Equal(oldEnd) {
				t.Fatal(revoked)
			}
			restored := applyRealAction(t, f, transition, "restore", 0, "")
			expected := "expired"
			if status == "suspended" {
				expected = status
			}
			if restored.Status != "succeeded" || restored.State.Subscription.Status != expected || !restored.State.Subscription.ExpiresAt.Equal(oldEnd) {
				t.Fatal("恢复篡改原期限或暂停状态", restored)
			}
			now := time.Now()
			renewed := applyRealAction(t, f, transition, "extend", 2, "")
			expected = "active"
			if status == "suspended" {
				expected = status
			}
			if renewed.Status != "succeeded" || renewed.State.Subscription.Status != expected || renewed.State.Subscription.ExpiresAt.Before(now.AddDate(0, 0, 2)) || !renewed.State.Subscription.StartsAt.Equal(oldStart) {
				t.Fatal("到期延期语义错误", renewed)
			}
			var usage string
			if err := f.db.QueryRow("SELECT daily_usage_usd FROM user_subscriptions").Scan(&usage); err != nil || usage != "17.0000000000" {
				t.Fatal("延期错误重置额度", usage, err)
			}
		})
	}
}

func TestPostgresRealSubscriptionLimitsAndDependencies(t *testing.T) {
	f, transition := realBusinessFixture(t)
	assigned := applyRealAction(t, f, transition, "assign", 36500, "")
	if assigned.Status != "succeeded" || !assigned.State.Subscription.ExpiresAt.Equal(service.MaxExpiresAt) {
		t.Fatal("未复用2099年上限", assigned)
	}
	// 缩短到当前或过去必须拒绝；原回执、期限和控制版本均保留。
	execBusiness(t, f.db, "UPDATE user_subscriptions SET expires_at=clock_timestamp()+interval '1 hour'")
	prior := readState(t, f)
	rejected := applyRealAction(t, f, transition, "extend", -1, "")
	if rejected.Status != "rejected" || readState(t, f).Revision != prior.Revision {
		t.Fatal("缩短使订阅到期", rejected)
	}
	execBusiness(t, f.db, "UPDATE groups SET subscription_type='standard'")
	rejected = applyRealAction(t, f, transition, "extend", 1, "")
	if rejected.Status != "rejected" || rejected.ErrorCode != "GROUP_UNAVAILABLE" {
		t.Fatal("非订阅分组被接受", rejected)
	}
	execBusiness(t, f.db, "UPDATE groups SET subscription_type='subscription'; UPDATE users SET status='disabled' WHERE id=501")
	command := inputCommand()
	command.Action, command.SubscriptionID, command.ExpectedRevision, command.Days = "extend", prior.Subscription.ID, prior.Revision, 1
	commands := countRows(t, f.db, "subscription_control_commands")
	if _, err := f.store.Apply(t.Context(), "fixture-app", command, grant, transition); err == nil {
		t.Fatal("停用管理员仍可修改")
	}
	if countRows(t, f.db, "subscription_control_commands") != commands || readState(t, f).Revision != prior.Revision {
		t.Fatal("管理员依赖失败留下终态或业务写入")
	}
}

func TestPostgresBackfillSelectsMostRecentlyRevoked(t *testing.T) {
	f := controlDatabase(t, func(db *sql.DB) {
		execBusiness(t, db, `INSERT INTO user_subscriptions(user_id,group_id,deleted_at) VALUES(101,4,clock_timestamp()-interval '1 hour');
  INSERT INTO user_subscriptions(user_id,group_id,deleted_at) VALUES(101,4,clock_timestamp()-interval '2 days');`)
	})
	state := readState(t, f)
	if state.Subscription.ID != 1 || state.Subscription.Status != "revoked" || state.Revision != "1" {
		t.Fatal("历史初始化错误选择更大ID而非最新撤销", state)
	}
}
