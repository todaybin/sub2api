//go:build unit

// Copyright 2026 成都众联思索科技有限公司。保留所有权利。
// 文件作用：验证新订阅控制协议同步失效L1、共享缓存及广播的真实服务路径。
package service

import (
	"context"
	"errors"
	"testing"

	control "github.com/Wei-Shaw/sub2api/internal/pkg/subscriptioncontrol"
	"github.com/dgraph-io/ristretto"
)

type subscriptionControlCache struct {
	BillingCache
	invalidationErr, publishErr error
	invalidations, publishes    int
	user, group                 int64
	key                         string
}

func (c *subscriptionControlCache) InvalidateSubscriptionCache(ctx context.Context, user, group int64) error {
	c.invalidations++
	c.user, c.group = user, group
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return c.invalidationErr
}
func (c *subscriptionControlCache) PublishSubscriptionCacheInvalidation(ctx context.Context, key string) error {
	c.publishes++
	c.key = key
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return c.publishErr
}
func (c *subscriptionControlCache) SubscribeSubscriptionCacheInvalidation(context.Context, func(string)) error {
	return nil
}

type subscriptionControlCacheWithoutBroadcast struct{ BillingCache }

func TestSubscriptionControlCachesRequireRuntimeDependencies(t *testing.T) {
	for _, svc := range []*SubscriptionService{
		nil, {}, {billingCacheService: &BillingCacheService{}},
		{billingCacheService: &BillingCacheService{cache: &subscriptionControlCacheWithoutBroadcast{}}},
	} {
		if err := svc.InvalidateSubscriptionControlCaches(t.Context(), 101, 4); !errors.Is(err, control.ErrStoreUnavailable) {
			t.Fatal("缺失依赖被当作缓存成功", err)
		}
	}
}

func TestSubscriptionControlCachesSynchronouslyInvalidateAndBroadcast(t *testing.T) {
	failed := errors.New("本地缓存测试故障")
	for _, stage := range []string{"success", "shared", "broadcast"} {
		t.Run(stage, func(t *testing.T) {
			l1, err := ristretto.NewCache(&ristretto.Config{NumCounters: 1000, MaxCost: 100, BufferItems: 64})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(l1.Close)
			key := subCacheKey(101, 4)
			l1.Set(key, "stale", 1)
			l1.Wait()
			if _, ok := l1.Get(key); !ok {
				t.Fatal("未建立L1测试前提")
			}
			cache := &subscriptionControlCache{}
			if stage == "shared" {
				cache.invalidationErr = failed
			}
			if stage == "broadcast" {
				cache.publishErr = failed
			}
			svc := &SubscriptionService{subCacheL1: l1, billingCacheService: &BillingCacheService{cache: cache}}
			err = svc.InvalidateSubscriptionControlCaches(t.Context(), 101, 4)
			if (stage == "success" && err != nil) || (stage != "success" && !errors.Is(err, failed)) {
				t.Fatal(stage, err)
			}
			if _, ok := l1.Get(key); ok {
				t.Fatal("返回后仍可读取旧L1")
			}
			if cache.invalidations != 1 || cache.user != 101 || cache.group != 4 {
				t.Fatal("失效了错误共享缓存", cache)
			}
			want := 1
			if stage == "shared" {
				want = 0
			}
			if cache.publishes != want || want == 1 && cache.key != key {
				t.Fatal("广播顺序或归属错误", cache)
			}
		})
	}
}
