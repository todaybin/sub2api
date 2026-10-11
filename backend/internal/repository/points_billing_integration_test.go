//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPointsBilling_FinalRequestDebtDedupRecoveryAndTopUp(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUserRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 2})
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeStandard, AllowPointsBilling: true, PointsPerUSD: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Name: "points", Key: uuid.NewString(), Quota: 10})
	_, err := repo.AdjustPoints(ctx, user.ID, 30, "add", uuid.NewString(), "initial")
	require.NoError(t, err)
	// Two concurrent attempts share the same user wallet, only one gets its last credit.
	ids := []string{uuid.NewString(), uuid.NewString()}
	out := make([]error, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, out[i] = repo.ReservePoints(ctx, ids[i], user.ID, key.ID, group.ID, 1)
		}(i)
	}
	wg.Wait()
	success := -1
	for i, e := range out {
		if e == nil {
			require.Equal(t, -1, success)
			success = i
		} else {
			require.True(t, errors.Is(e, service.ErrInsufficientPoints))
		}
	}
	require.NotEqual(t, -1, success)
	cmd := &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, PointsReservationID: ids[success], PointsGroupID: group.ID, PointsPerUSD: 100, PointsCostUSD: 1, APIKeyQuotaCost: 1, BillingType: service.BillingTypePoints}
	cmd.Normalize()
	// Existing reservations keep their group rate after the administrator edits it.
	_, err = client.Group.UpdateOneID(group.ID).SetPointsPerUsd(250).Save(ctx)
	require.NoError(t, err)
	body, err := json.Marshal(cmd)
	require.NoError(t, err)
	// Simulate a restart after the durable settlement was saved but before commit.
	_, err = integrationDB.Exec(`UPDATE points_reservations SET state='pending',command=$1 WHERE id=$2`, string(body), cmd.PointsReservationID)
	require.NoError(t, err)
	require.NoError(t, repo.ReleasePoints(ctx, cmd.PointsReservationID))
	var recovered int
	require.NoError(t, repo.RecoverPointsWithEffects(ctx, func(recoveredCmd *service.UsageBillingCommand) {
		recovered++
		require.Equal(t, cmd.RequestID, recoveredCmd.RequestID)
	}))
	require.NoError(t, repo.RecoverPointsWithEffects(ctx, func(*service.UsageBillingCommand) { recovered++ }))
	require.Equal(t, 1, recovered)
	billing := &usageBillingRepository{db: integrationDB}
	result, err := billing.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	refreshed, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, -70.0, refreshed.PointsBalance)
	require.Zero(t, refreshed.PointsReserved)
	require.Equal(t, 2.0, refreshed.Balance)
	_, err = repo.AdjustPoints(ctx, user.ID, 100, "add", uuid.NewString(), "topup")
	require.NoError(t, err)
	available, err := repo.PointsAvailable(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 30.0, available)
	entries, total, err := repo.ListPointsLedger(ctx, user.ID, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, entries, 3)
	require.Equal(t, -100.0, entries[1].Amount)
	require.Equal(t, 1.0, entries[1].CostUSD)
	require.Equal(t, 100.0, entries[1].PointsPerUSD)
	// A later request uses the edited group rate.
	nextID := uuid.NewString()
	rate, err := repo.ReservePoints(ctx, nextID, user.ID, key.ID, group.ID, 0.02)
	require.NoError(t, err)
	require.Equal(t, 250.0, rate)
	require.NoError(t, repo.ReleasePoints(ctx, nextID))
}
