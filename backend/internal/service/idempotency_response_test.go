package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdempotencySensitiveResponseEncryptedReplay(t *testing.T) {
	repo := newInMemoryIdempotencyRepo()
	cfg := DefaultIdempotencyConfig()
	cfg.ResponseEncryptionSecret = "persistent-jwt-secret"
	coordinator := NewIdempotencyCoordinator(repo, cfg)
	opts := IdempotencyExecuteOptions{Scope: "session.issue", ActorScope: "integration:app", Method: "POST", Route: "/users/1/token", IdempotencyKey: "session-1", Payload: map[string]any{"user_id": 1}, SensitiveResponse: true}
	calls := 0
	execute := func(context.Context) (any, error) {
		calls++
		return map[string]any{"access_token": "original-jwt", "refresh_token": "original-refresh"}, nil
	}
	first, err := coordinator.Execute(context.Background(), opts, execute)
	require.NoError(t, err)
	var stored *IdempotencyRecord
	for _, record := range repo.data {
		stored = record
	}
	require.NotNil(t, stored)
	require.NotContains(t, *stored.ResponseBody, "original-jwt")
	require.NotContains(t, *stored.ResponseBody, "original-refresh")
	// Another process with the same persistent secret recovers the original session.
	replayed, err := NewIdempotencyCoordinator(repo, cfg).Execute(context.Background(), opts, execute)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Data, replayed.Data)
	require.Equal(t, 1, calls)

	cfg.ResponseEncryptionSecret = "different-jwt-secret"
	_, err = NewIdempotencyCoordinator(repo, cfg).Execute(context.Background(), opts, execute)
	require.ErrorIs(t, err, ErrIdempotencyStoreUnavail)
	require.Equal(t, 1, calls)
	// A copied ciphertext cannot be used for a different request identity.
	_, err = coordinator.decodeReplayResponse(stored.ResponseBody, true, "different-fingerprint")
	require.Error(t, err)
	tampered := *stored.ResponseBody + "A"
	_, err = coordinator.decodeReplayResponse(&tampered, true, stored.RequestFingerprint)
	require.Error(t, err)
}

func TestIdempotencySensitiveResponseRequiresEncryption(t *testing.T) {
	coordinator := NewIdempotencyCoordinator(newInMemoryIdempotencyRepo(), DefaultIdempotencyConfig())
	executed := false
	_, err := coordinator.Execute(context.Background(), IdempotencyExecuteOptions{SensitiveResponse: true, IdempotencyKey: "key"}, func(context.Context) (any, error) {
		executed = true
		return nil, nil
	})
	require.ErrorIs(t, err, ErrIdempotencyStoreUnavail)
	require.False(t, executed)
}
