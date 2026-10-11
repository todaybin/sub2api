//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIntegrationUsagePointsRespectLedgerAndFilters(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com"})
	other := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "points"})
	key2 := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: uuid.NewString(), Name: "other"})
	otherKey := mustCreateApiKey(t, client, &service.APIKey{UserID: other.ID, Key: uuid.NewString(), Name: "other-user"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString()})
	now := time.Now().UTC()
	insert := func(userID, keyID int64, at time.Time, billing int8, rate, amount float64) string {
		requestID := uuid.NewString()
		_, err := repo.Create(ctx, &service.UsageLog{UserID: userID, APIKeyID: keyID, AccountID: account.ID, RequestID: requestID,
			Model: "public-model", CreatedAt: at, BillingType: billing, TotalCost: 1, ActualCost: 1})
		require.NoError(t, err)
		if amount > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO points_ledger(user_id,operation_id,kind,amount,balance_after,cost_usd,points_per_usd,request_id,api_key_id)
			 VALUES($1,$2,'usage',$3,0,1,$4,$5,$6)`, userID, uuid.NewString(), -amount, rate, requestID, keyID)
			require.NoError(t, err)
		}
		return requestID
	}
	insert(user.ID, key.ID, now, service.BillingTypePoints, 100, 100)
	insert(user.ID, key.ID, now, service.BillingTypePoints, 250, 250)
	insert(user.ID, key.ID, now, service.BillingTypeBalance, 0, 0)
	insert(user.ID, key.ID, now, service.BillingTypeSubscription, 0, 0)
	insert(user.ID, key2.ID, now, service.BillingTypePoints, 400, 400)
	insert(user.ID, key.ID, now.Add(-2*time.Hour), service.BillingTypePoints, 1000, 1000)
	insert(other.ID, otherKey.ID, now, service.BillingTypePoints, 900, 900)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	filters := usagestats.UsageLogFilters{UserID: user.ID, APIKeyID: key.ID, StartTime: &start, EndTime: &end}
	stats, err := repo.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.Equal(t, int64(4), stats.TotalRequests)
	require.Equal(t, 350.0, stats.TotalPointsCost)
	logs, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	var spent float64
	for _, log := range logs {
		spent += log.PointsCost
		if log.BillingType != service.BillingTypePoints {
			require.Zero(t, log.PointsCost)
		}
	}
	require.Equal(t, stats.TotalPointsCost, spent)
	filters.APIKeyID = 0
	stats, err = repo.GetStatsWithFilters(ctx, filters)
	require.NoError(t, err)
	require.Equal(t, 750.0, stats.TotalPointsCost)
}

func TestIntegrationExternalIDMigrationPreservesNotesAndDuplicates(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUserRepositoryWithSQL(client, tx)
	oldNotes := "人工备注\n[integration_external_id:partner:123]"
	legacy := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Notes: oldNotes})
	ambiguous := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", Notes: "[integration_external_id:a]\n[integration_external_id:b]"})
	migration, err := os.ReadFile("../../migrations/244_integration_external_id.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	user, err := repo.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, "partner:123", user.ExternalID)
	require.Equal(t, oldNotes, user.Notes)
	user, err = repo.GetByID(ctx, ambiguous.ID)
	require.NoError(t, err)
	require.Empty(t, user.ExternalID)
	for i := 0; i < 2; i++ {
		user = &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", ExternalID: "自由编号]\n同编号", Notes: "  原样备注  ", Role: service.RoleUser, Status: service.StatusActive}
		require.NoError(t, repo.Create(ctx, user))
		read, err := repo.GetByID(ctx, user.ID)
		require.NoError(t, err)
		require.Equal(t, user.ExternalID, read.ExternalID)
		require.Equal(t, user.Notes, read.Notes)
	}
}
