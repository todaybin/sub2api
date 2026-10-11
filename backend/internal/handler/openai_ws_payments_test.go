//go:build unit

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnPayments_IsolateTurnsAndReleaseOnce(t *testing.T) {
	var payments openAIWSTurnPayments
	type turnKey struct{}
	var reserved, released int
	reserve := func(turn int) func() (context.Context, func(), error) {
		return func() (context.Context, func(), error) {
			reserved++
			return context.WithValue(context.Background(), turnKey{}, turn), func() { released++ }, nil
		}
	}
	require.NoError(t, payments.begin(1, reserve(1)))
	require.NoError(t, payments.begin(1, reserve(1)))
	require.NoError(t, payments.begin(2, reserve(2)))
	require.Equal(t, 2, reserved)
	require.Equal(t, 1, payments.context(1, nil).Value(turnKey{}))
	require.Equal(t, 2, payments.context(2, nil).Value(turnKey{}))
	payments.release(1)
	payments.release(1)
	require.Equal(t, 1, released)
	payments.close()
	require.Equal(t, 2, released)
	payments.close()
	require.Equal(t, 2, released)
	// A failed attempt can reserve afresh when retrying its response.
	require.NoError(t, payments.begin(1, reserve(1)))
	require.Equal(t, 3, reserved)
	payments.close()
}
