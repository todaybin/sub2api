package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestSmartGroupAffinityAtomicOwnershipAndExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(service.SmartGroupAffinityCache)
	ctx := context.Background()
	var wg sync.WaitGroup
	wins := make(chan int64, 16)
	errs := make(chan error, 16)
	for i := int64(1); i <= 16; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			winner, err := cache.ClaimSmartGroup(ctx, 1, "session", 0, id, time.Hour)
			wins <- winner
			errs <- err
		}(i)
	}
	wg.Wait()
	close(wins)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	owner, err := cache.GetSmartGroup(ctx, 1, "session")
	require.NoError(t, err)
	require.Positive(t, owner)
	for winner := range wins {
		require.Equal(t, owner, winner)
	}
	other, err := cache.GetSmartGroup(ctx, 2, "session")
	require.NoError(t, err)
	require.Zero(t, other)
	next, err := cache.ClaimSmartGroup(ctx, 1, "session", owner, 99, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(99), next)
	stale, err := cache.ClaimSmartGroup(ctx, 1, "session", owner, 77, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(99), stale)
	server.FastForward(2 * time.Hour)
	expired, err := cache.GetSmartGroup(ctx, 1, "session")
	require.NoError(t, err)
	require.Zero(t, expired)
	next, err = cache.ClaimSmartGroup(ctx, 1, "session", 0, 77, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(77), next)
}
