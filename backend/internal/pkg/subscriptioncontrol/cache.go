// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：以持久租约恢复订阅缓存失效，绝不重执行业务命令。
package subscriptioncontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrCacheLeaseLost 表示原处理租约已变更，旧处理者不能覆盖新结果。
var ErrCacheLeaseLost = errors.New("SUBSCRIPTION_CONTROL_CACHE_LEASE_LOST")

// CacheInvalidator 必须同步失效本地、共享缓存及跨实例通知，并遵守上下文超时。
type CacheInvalidator func(context.Context, int64, int64) error

type cacheClaim struct {
	app, id, lease string
	user, group    int64
	attempts       int
}

// RunCacheJob 每次最多处理一个持久待办；至多五次，过期租约可恢复，耗尽后进入人工核查。
// 调用方负责现有生命周期调度，本方法不启动goroutine或创建连接池。
func (s *Store) RunCacheJob(ctx context.Context, invalidate CacheInvalidator) (bool, error) {
	if s == nil || s.db == nil || invalidate == nil {
		return false, ErrStoreUnavailable
	}
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	claim, worked, err := s.claimCacheJob(ctx)
	if err != nil || !worked || claim == nil {
		return worked, err
	}
	effectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	effectErr := invalidate(effectCtx, claim.user, claim.group)
	// 即使回调误返回nil，已超时或取消也不能确认缓存恢复成功。
	if effectErr == nil {
		effectErr = effectCtx.Err()
	}
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finishCancel()
	if err = s.finishCacheJob(finishCtx, *claim, effectErr == nil); err != nil {
		return true, err
	}
	if effectErr != nil {
		return true, fmt.Errorf("订阅缓存失效未完成，保留恢复依据: %w", effectErr)
	}
	return true, nil
}

func (s *Store) claimCacheJob(ctx context.Context) (*cacheClaim, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("开启订阅缓存领取事务失败: %w", err)
	}
	defer tx.Rollback()
	var c cacheClaim
	err = tx.QueryRowContext(ctx, `SELECT j.app_id,j.command_id::text,j.attempts,c.user_id,c.group_id
  FROM subscription_control_cache_jobs j JOIN subscription_control_commands c
   ON c.app_id=j.app_id AND c.command_id=j.command_id
  WHERE c.status='succeeded' AND
   ((j.status='pending' AND j.next_at<=clock_timestamp()) OR
    (j.status='running' AND j.lease_until<=clock_timestamp()))
  ORDER BY j.next_at,j.app_id,j.command_id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`).Scan(&c.app, &c.id, &c.attempts, &c.user, &c.group)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("领取订阅缓存待办失败: %w", err)
	}
	if c.attempts >= 5 {
		_, err = tx.ExecContext(ctx, `UPDATE subscription_control_cache_jobs SET status='review',
   lease_id=NULL,lease_until=NULL,updated_at=clock_timestamp()
   WHERE app_id=$1 AND command_id=$2`, c.app, c.id)
	} else {
		c.attempts++
		c.lease = uuid.NewString()
		_, err = tx.ExecContext(ctx, `UPDATE subscription_control_cache_jobs SET status='running',
   attempts=$3,lease_id=$4,lease_until=clock_timestamp()+interval '15 seconds',updated_at=clock_timestamp()
   WHERE app_id=$1 AND command_id=$2`, c.app, c.id, c.attempts, c.lease)
	}
	if err != nil {
		return nil, false, fmt.Errorf("保存订阅缓存租约失败: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("提交订阅缓存租约失败: %w", err)
	}
	if c.attempts >= 5 && c.lease == "" {
		return nil, true, nil
	}
	return &c, true, nil
}

func (s *Store) finishCacheJob(ctx context.Context, c cacheClaim, success bool) error {
	status := "pending"
	if success {
		status = "completed"
	} else if c.attempts >= 5 {
		status = "review"
	}
	// 退避只影响缓存恢复；永久业务回执与订阅记录不被写入。
	result, err := s.db.ExecContext(ctx, `UPDATE subscription_control_cache_jobs SET
  status=$5::varchar,lease_id=NULL,lease_until=NULL,
  next_at=clock_timestamp()+make_interval(secs=>$6),
  updated_at=clock_timestamp(),completed_at=CASE WHEN $5::varchar='completed' THEN clock_timestamp() ELSE NULL END
  WHERE app_id=$1 AND command_id=$2 AND status='running' AND lease_id=$3 AND attempts=$4`,
		c.app, c.id, c.lease, c.attempts, status, c.attempts*c.attempts*5)
	if err != nil {
		return fmt.Errorf("保存订阅缓存恢复结果失败: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("核验订阅缓存恢复结果失败: %w", err)
	}
	if n != 1 {
		return ErrCacheLeaseLost
	}
	return nil
}
