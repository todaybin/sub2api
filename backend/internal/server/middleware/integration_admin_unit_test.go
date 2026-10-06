//go:build unit

package middleware

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIntegrationAdminGateway(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	testIntegrationAdminGateway(t, client, false)
}

func TestIntegrationAdminGatewayWithoutRedis(t *testing.T) {
	testIntegrationAdminGateway(t, nil, false)
}

func TestIntegrationAdminGatewayRedisFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	server.SetError("ERR unavailable")
	testIntegrationAdminGateway(t, client, true)
}
