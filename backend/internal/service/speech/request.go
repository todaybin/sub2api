// Copyright (c) 2026 WorkMesh. 保留所有权利。
package speech

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
)

type providerRequestKey struct{}

// 固定请求标识用于核对，不能视为供应商提供了幂等重试保证。
func ProviderRequestID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}
func WithProviderRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, providerRequestKey{}, id)
}
func providerRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(providerRequestKey{}).(string); ok && id != "" {
		return id
	}
	return uuid.NewString()
}
