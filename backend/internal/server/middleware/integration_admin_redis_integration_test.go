//go:build integration

package middleware

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdminGatewayRealRedis(t *testing.T) {
	addr := os.Getenv("SUB2API_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set SUB2API_TEST_REDIS_ADDR to run the gateway against a real Redis server")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	testIntegrationAdminGateway(t, client, false)
}
