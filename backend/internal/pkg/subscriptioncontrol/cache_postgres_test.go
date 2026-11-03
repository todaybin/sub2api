//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：验证缓存恢复的持久租约、原键业务隔离和有限重试。
package subscriptioncontrol_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/google/uuid"
)

func cacheStatus(t *testing.T, f databaseFixture) (string, int) {
	t.Helper()
	var status string
	var attempts int
	if err := f.db.QueryRow("SELECT status,attempts FROM subscription_control_cache_jobs").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, attempts
}

func TestPostgresCacheRecoveryDoesNotRepeatBusiness(t *testing.T) {
	f := controlDatabase(t, nil)
	original := mustApply(t, f, inputCommand())
	if worked, err := f.store.RunCacheJob(t.Context(), nil); err == nil || worked {
		t.Fatal("空依赖仍领取任务", worked, err)
	}
	status, attempts := cacheStatus(t, f)
	if status != "pending" || attempts != 0 {
		t.Fatal(status, attempts)
	}
	var calls atomic.Int32
	invalidate := func(ctx context.Context, user, group int64) error {
		if user != 101 || group != 4 {
			t.Error("错误缓存归属", user, group)
		}
		if calls.Add(1) == 1 {
			return errors.New("本地测试失效失败")
		}
		return nil
	}
	if worked, err := f.store.RunCacheJob(t.Context(), invalidate); err == nil || !worked {
		t.Fatal(worked, err)
	}
	status, attempts = cacheStatus(t, f)
	if status != "pending" || attempts != 1 {
		t.Fatal(status, attempts)
	}
	if worked, err := f.store.RunCacheJob(t.Context(), invalidate); err != nil || worked {
		t.Fatal("未遵守退避", worked, err)
	}
	execBusiness(t, f.db, "UPDATE subscription_control_cache_jobs SET next_at=clock_timestamp()-interval '1 second'")
	if worked, err := f.store.RunCacheJob(t.Context(), invalidate); err != nil || !worked {
		t.Fatal(worked, err)
	}
	status, attempts = cacheStatus(t, f)
	if status != "completed" || attempts != 2 || calls.Load() != 2 {
		t.Fatal(status, attempts, calls.Load())
	}
	if readState(t, f).Revision != original.State.Revision || countRows(t, f.db, "subscription_control_commands") != 1 {
		t.Fatal("缓存恢复重做了业务")
	}
	if worked, err := f.store.RunCacheJob(t.Context(), invalidate); err != nil || worked {
		t.Fatal("终态任务重执行", worked, err)
	}
}

func TestPostgresCacheRecoveryLeaseAndAttemptLimit(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "过期租约恢复", true: "第五次未知转人工"}[exhausted], func(t *testing.T) {
			f := controlDatabase(t, nil)
			mustApply(t, f, inputCommand())
			attempts := 1
			if exhausted {
				attempts = 5
			}
			execBusiness(t, f.db, "UPDATE subscription_control_cache_jobs SET status='running',attempts=$1,lease_id=$2,lease_until=clock_timestamp()-interval '1 second'", attempts, uuid.NewString())
			calls := 0
			worked, err := f.store.RunCacheJob(t.Context(), func(context.Context, int64, int64) error { calls++; return nil })
			if err != nil || !worked {
				t.Fatal(worked, err)
			}
			status, n := cacheStatus(t, f)
			if exhausted {
				if status != "review" || n != 5 || calls != 0 {
					t.Fatal(status, n, calls)
				}
			} else if status != "completed" || n != 2 || calls != 1 {
				t.Fatal(status, n, calls)
			}
		})
	}
	f := controlDatabase(t, nil)
	mustApply(t, f, inputCommand())
	for i := 0; i < 5; i++ {
		execBusiness(t, f.db, "UPDATE subscription_control_cache_jobs SET next_at=clock_timestamp()-interval '1 second'")
		if worked, err := f.store.RunCacheJob(t.Context(), func(context.Context, int64, int64) error { return errors.New("缓存不可用") }); err == nil || !worked {
			t.Fatal(worked, err)
		}
	}
	status, n := cacheStatus(t, f)
	if status != "review" || n != 5 {
		t.Fatal(status, n)
	}
	if worked, err := f.store.RunCacheJob(t.Context(), func(context.Context, int64, int64) error { t.Error("越过最大尝试"); return nil }); err != nil || worked {
		t.Fatal(worked, err)
	}
}

func TestPostgresCacheRecoveryCannotOverwriteNewLease(t *testing.T) {
	f := controlDatabase(t, nil)
	mustApply(t, f, inputCommand())
	newer := uuid.NewString()
	worked, err := f.store.RunCacheJob(t.Context(), func(context.Context, int64, int64) error {
		execBusiness(t, f.db, "UPDATE subscription_control_cache_jobs SET lease_id=$1,attempts=attempts+1", newer)
		return nil
	})
	if !worked || !errors.Is(err, control.ErrCacheLeaseLost) {
		t.Fatal(worked, err)
	}
	var lease string
	if e := f.db.QueryRow("SELECT lease_id::text FROM subscription_control_cache_jobs").Scan(&lease); e != nil || lease != newer {
		t.Fatal("旧租约覆盖了新结果", lease, e)
	}
}

func TestPostgresCacheRecoveryConcurrentClaim(t *testing.T) {
	f := controlDatabase(t, nil)
	mustApply(t, f, inputCommand())
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		_, err := f.store.RunCacheJob(t.Context(), func(ctx context.Context, user, group int64) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("首次领取失败", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	if worked, err := f.store.RunCacheJob(t.Context(), func(context.Context, int64, int64) error { t.Error("活跃租约重复领取"); return nil }); err != nil || worked {
		t.Fatal(worked, err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status, n := cacheStatus(t, f)
	if status != "completed" || n != 1 {
		t.Fatal(status, n)
	}
}

// TestPostgresCacheCancelledCallbackNotAcknowledged 验证取消后即使回调返回nil也不产生假完成。
func TestPostgresCacheCancelledCallbackNotAcknowledged(t *testing.T) {
	f := controlDatabase(t, nil)
	mustApply(t, f, inputCommand())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	worked, err := f.store.RunCacheJob(ctx, func(context.Context, int64, int64) error { cancel(); return nil })
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatal(worked, err)
	}
	status, n := cacheStatus(t, f)
	if status != "pending" || n != 1 {
		t.Fatal("取消被记作缓存完成", status, n)
	}
}
