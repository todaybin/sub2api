// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：在既有订阅服务生命周期中装配持久缓存恢复，不创建新服务或连接池。
package service

import (
	"context"
	"database/sql"
	"log"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
)

type subscriptionControlRuntime struct {
	store  *control.Store
	cancel context.CancelFunc
	done   chan struct{}
}

// ProvideSubscriptionService 复用原构造器及数据库，生产装配才启动受Stop管理的缓存恢复。
func ProvideSubscriptionService(groups GroupRepository, subs UserSubscriptionRepository, billing *BillingCacheService, client *dbent.Client, cfg *config.Config, db *sql.DB) *SubscriptionService {
	svc := NewSubscriptionService(groups, subs, billing, client, cfg)
	if db == nil {
		return svc
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &subscriptionControlRuntime{store: control.NewStore(db), cancel: cancel, done: make(chan struct{})}
	svc.controlRuntime = r
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastFailure time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := r.store.RunCacheJob(ctx, svc.InvalidateSubscriptionControlCaches)
				if err != nil && ctx.Err() == nil && time.Since(lastFailure) >= time.Minute {
					// 仅记录固定失败类别，任务事实由数据库保留；不打印配置、凭据或回调原始错误。
					log.Print("订阅缓存恢复暂未完成，等待有界重试或人工核查")
					lastFailure = time.Now()
				}
			}
		}
	}()
	return svc
}

// SubscriptionControlStore 只供已装配服务的协议控制器使用，不创建连接或后台任务。
func (s *SubscriptionService) SubscriptionControlStore() *control.Store {
	if s == nil || s.controlRuntime == nil {
		return nil
	}
	return s.controlRuntime.store
}
func (s *SubscriptionService) stopSubscriptionControlRuntime() {
	if s.controlRuntime != nil {
		s.controlRuntime.cancel()
		<-s.controlRuntime.done
	}
}
