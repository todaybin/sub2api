//go:build integration

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：验证正式服务装配的缓存恢复可取消、可重复停止，数据库等待不会泄漏生命周期。
package subscriptioncontrol_test

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestPostgresSubscriptionRuntimeStopsDuringDatabaseWait(t *testing.T) {
	f, _ := accessFixture(t)
	f.db.SetMaxOpenConns(1)
	held, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	svc := service.ProvideSubscriptionService(nil, nil, nil, nil, nil, f.db)
	t.Cleanup(svc.Stop)
	if svc.SubscriptionControlStore() == nil {
		t.Fatal("正式provider未装配控制存储")
	}
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for f.db.Stats().WaitCount == 0 {
		select {
		case <-deadline.C:
			t.Fatal("缓存worker没有进入数据库等待")
		case <-poll.C:
		}
	}
	done := make(chan struct{})
	go func() { svc.Stop(); svc.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止没有取消worker数据库等待")
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var count int
	if err = f.db.QueryRowContext(ctx, "SELECT count(*) FROM subscription_control_cache_jobs").Scan(&count); err != nil || count != 0 {
		t.Fatal("取消时产生额外恢复工作", count, err)
	}
}

func TestSubscriptionRuntimeWithoutDatabaseStaysUnavailable(t *testing.T) {
	var empty *service.SubscriptionService
	if empty.SubscriptionControlStore() != nil {
		t.Fatal("空服务提供了可写存储")
	}
	svc := service.ProvideSubscriptionService(nil, nil, nil, nil, nil, nil)
	defer svc.Stop()
	if svc.SubscriptionControlStore() != nil {
		t.Fatal("缺数据库却装配控制存储")
	}
	svc.Stop()
}

func TestPostgresSubscriptionRuntimeCompletesRealCacheRecovery(t *testing.T) {
	f, transition := accessFixture(t)
	saveScope(t, f, grantCommand())
	command := inputCommand()
	body, _ := json.Marshal(command)
	authorize := f.store.GrantAuthorizer(scopeApp, 501, signedVerifier(t, "PUT", controlInternal+"/commands/"+command.CommandID, body))
	receipt, err := f.store.Apply(t.Context(), scopeApp, command, authorize, transition)
	if err != nil || receipt.Status != "succeeded" {
		t.Fatal(receipt, err)
	}
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	pubsub := client.Subscribe(t.Context(), "subscription:cache:invalidate")
	defer pubsub.Close()
	if _, err = pubsub.Receive(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = client.Set(t.Context(), "billing:sub:101:4", "stale-fixture", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	billing := service.NewBillingCacheService(repository.NewBillingCache(client), nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	svc := service.ProvideSubscriptionService(nil, nil, billing, nil, nil, f.db)
	t.Cleanup(svc.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	message, err := pubsub.ReceiveMessage(ctx)
	if err != nil || message.Payload != "sub:101:4" {
		t.Fatal("未通过真实Redis适配器广播", message, err)
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	var status string
	var attempts int
	for {
		err = f.db.QueryRowContext(ctx, "SELECT status,attempts FROM subscription_control_cache_jobs WHERE app_id=$1 AND command_id=$2", scopeApp, command.CommandID).Scan(&status, &attempts)
		if err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("自动缓存恢复未持久确认", status, ctx.Err())
		case <-poll.C:
		}
	}
	svc.Stop()
	if attempts != 1 {
		t.Fatal("缓存恢复次数异常", attempts)
	}
	if exists, e := client.Exists(t.Context(), "billing:sub:101:4").Result(); e != nil || exists != 0 {
		t.Fatal("共享缓存未删除", exists, e)
	}
	var revision string
	var commands int
	if err = f.db.QueryRow("SELECT revision::text FROM subscription_control_slots WHERE user_id=101 AND group_id=4").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM subscription_control_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if revision != "1" || commands != 1 {
		t.Fatal("缓存worker重做业务", revision, commands)
	}
}
