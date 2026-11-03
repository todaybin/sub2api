// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：把既有订阅规则接入原子控制事务，每个成功动作只写一次订阅。
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// SubscriptionControlTransition 使用已认证的本地管理员，不把平台actor_uid冒充上游用户。
// 返回的动作只能交给Store.Apply，并由调用方提供独立的事务内集成归属授权。
func (s *SubscriptionService) SubscriptionControlTransition(adminID int64) control.Transition {
	return func(ctx context.Context, tx *sql.Tx, c control.SubscriptionControlCommand) (string, error) {
		if s == nil || tx == nil || adminID < 1 || c.Validate() != nil {
			return "", control.ErrSubscriptionControlInput
		}
		var id int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=$1 AND role='admin' AND status='active' AND deleted_at IS NULL FOR SHARE", adminID).Scan(&id)
		if err != nil {
			return "", fmt.Errorf("核验订阅本地管理员失败: %w", err)
		}
		err = tx.QueryRowContext(ctx, "SELECT id FROM groups WHERE id=$1 AND subscription_type='subscription' AND status='active' AND deleted_at IS NULL FOR SHARE", c.GroupID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "GROUP_UNAVAILABLE", nil
		}
		if err != nil {
			return "", fmt.Errorf("核验订阅分组失败: %w", err)
		}

		err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=$1 AND role='user' AND status='active' AND deleted_at IS NULL FOR SHARE", c.UserID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "SUBSCRIPTION_STATE_INVALID", nil
		}
		if err != nil {
			return "", fmt.Errorf("核验订阅目标账号状态失败: %w", err)
		}

		now := time.Now()
		if s.now != nil {
			now = s.now()
		}
		now = now.In(timezone.Location())
		if c.Action == "assign" {
			end := controlExpiresAt(now, c.Days)
			if !end.After(now) {
				return "SUBSCRIPTION_STATE_INVALID", nil
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO user_subscriptions
    (user_id,group_id,starts_at,expires_at,status,assigned_by,assigned_at,created_at,updated_at)
    VALUES($1,$2,$3,$4,'active',$5,$3,$3,$3)`, c.UserID, c.GroupID, now, end, adminID)
			if err != nil {
				return "", fmt.Errorf("原子分配订阅失败: %w", err)
			}
			return "", nil
		}

		var starts, expires time.Time
		var status string
		var deleted sql.NullTime
		err = tx.QueryRowContext(ctx, `SELECT starts_at,expires_at,status,deleted_at FROM user_subscriptions
   WHERE id=$1 AND user_id=$2 AND group_id=$3 FOR UPDATE`, c.SubscriptionID, c.UserID, c.GroupID).Scan(&starts, &expires, &status, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return "SUBSCRIPTION_NOT_FOUND", nil
		}
		if err != nil {
			return "", fmt.Errorf("锁定原订阅失败: %w", err)
		}
		if (c.Action == "restore") != deleted.Valid {
			return "SUBSCRIPTION_STATE_INVALID", nil
		}
		if status != "active" && status != "expired" && status != "suspended" {
			return "SUBSCRIPTION_STATE_INVALID", nil
		}
		var result sql.Result
		switch c.Action {
		case "extend":
			expired := !expires.After(now)
			if expired && c.Days < 0 {
				return "SUBSCRIPTION_STATE_INVALID", nil
			}
			base := expires.In(timezone.Location())
			if expired {
				base = now
			}
			end := controlExpiresAt(base, c.Days)
			if !end.After(now) || !end.After(starts) {
				return "SUBSCRIPTION_STATE_INVALID", nil
			}
			// 到期延期不重置消费窗口，暂停状态保留；复用旧ExtendSubscription语义。
			if status == "expired" {
				status = "active"
			}
			result, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET expires_at=$1,status=$2,updated_at=$3 WHERE id=$4 AND deleted_at IS NULL", end, status, now, c.SubscriptionID)
		case "revoke":
			result, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at=$1,updated_at=$1 WHERE id=$2 AND deleted_at IS NULL", now, c.SubscriptionID)
		case "restore":
			var other int64
			err = tx.QueryRowContext(ctx, "SELECT id FROM user_subscriptions WHERE user_id=$1 AND group_id=$2 AND deleted_at IS NULL LIMIT 1", c.UserID, c.GroupID).Scan(&other)
			if err == nil {
				return "SUBSCRIPTION_STATE_INVALID", nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("检查恢复订阅冲突失败: %w", err)
			}
			if status == "active" && !expires.After(now) {
				status = "expired"
			}
			result, err = tx.ExecContext(ctx, "UPDATE user_subscriptions SET deleted_at=NULL,status=$1,updated_at=$2 WHERE id=$3 AND deleted_at IS NOT NULL", status, now, c.SubscriptionID)
		case "reset_quota":
			daily := c.ResetPeriod == "daily" || c.ResetPeriod == "all"
			weekly := c.ResetPeriod == "weekly" || c.ResetPeriod == "all"
			monthly := c.ResetPeriod == "monthly" || c.ResetPeriod == "all"
			result, err = tx.ExecContext(ctx, `UPDATE user_subscriptions SET
    daily_usage_usd=CASE WHEN $1 THEN 0 ELSE daily_usage_usd END,
    weekly_usage_usd=CASE WHEN $2 THEN 0 ELSE weekly_usage_usd END,
    monthly_usage_usd=CASE WHEN $3 THEN 0 ELSE monthly_usage_usd END,
    daily_window_start=CASE WHEN $1 THEN $4 ELSE daily_window_start END,
    weekly_window_start=CASE WHEN $2 THEN $5 ELSE weekly_window_start END,
    monthly_window_start=CASE WHEN $3 THEN $5 ELSE monthly_window_start END,updated_at=$5
    WHERE id=$6 AND deleted_at IS NULL`, daily, weekly, monthly, timezone.StartOfDay(now), now, c.SubscriptionID)
		default:
			return "", control.ErrSubscriptionControlInput
		}
		if err != nil {
			return "", fmt.Errorf("执行订阅%s失败: %w", c.Action, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("核验订阅变更行数失败: %w", err)
		}
		if affected != 1 {
			return "", control.ErrStoreUnavailable
		}
		return "", nil
	}
}

func controlExpiresAt(base time.Time, days int) time.Time {
	end := base.AddDate(0, 0, days)
	if end.After(MaxExpiresAt) {
		return MaxExpiresAt
	}
	return end
}

// InvalidateSubscriptionControlCaches 同步完成新控制协议所需缓存失效；缺少共享依赖时保持待恢复。
func (s *SubscriptionService) InvalidateSubscriptionControlCaches(ctx context.Context, userID, groupID int64) error {
	if s == nil || s.billingCacheService == nil || s.billingCacheService.cache == nil || userID < 1 || groupID < 1 {
		return control.ErrStoreUnavailable
	}
	if _, ok := s.billingCacheService.cache.(subscriptionCacheInvalidationPubSub); !ok {
		return control.ErrStoreUnavailable
	}
	s.InvalidateSubCacheSync(userID, groupID)
	if err := s.billingCacheService.InvalidateSubscription(ctx, userID, groupID); err != nil {
		return fmt.Errorf("失效订阅共享缓存失败: %w", err)
	}
	if err := s.billingCacheService.PublishSubscriptionCacheInvalidation(ctx, subCacheKey(userID, groupID)); err != nil {
		return fmt.Errorf("广播订阅缓存失效失败: %w", err)
	}
	return nil
}
