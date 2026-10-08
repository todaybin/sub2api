//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBalanceSchedulingManualOverride(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformCodeBuddy} {
		for _, mode := range []string{"automatic", "single manual", "bulk manual", "import manual", "legacy", "stale probe"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				tx := testEntTx(t)
				ctx = dbent.NewTxContext(ctx, tx)
				repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
				kind := service.AccountTypeAPIKey
				if platform == service.PlatformCodeBuddy {
					kind = service.AccountTypeOAuth
				}
				a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "balance-test", Platform: platform, Type: kind, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{service.UpstreamBillingProbeEnabledExtraKey: true}})
				apply := func(expected *service.Account, disable, restore bool) error {
					if platform == service.PlatformCodeBuddy {
						return repo.UpdateCodeBuddyBalanceScheduling(ctx, expected, service.CodeBuddyCheckinSettings{Enabled: true, LastCreditRemaining: 5}, disable, restore)
					}
					balance := 5.0
					if disable {
						balance = 0
					}
					return repo.UpdateUpstreamBillingProbeSnapshot(ctx, expected, &service.UpstreamBillingProbeSnapshot{Status: service.UpstreamBillingProbeStatusOK, Data: map[string]any{"billing_mode": "balance", "balance": balance}, LastAttemptAt: time.Now(), AutoDisableZeroBalance: disable, AutoRestoreBalance: restore}, nil)
				}
				outboxCount := func() int {
					rows, err := tx.Client().QueryContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", a.ID)
					require.NoError(t, err)
					defer rows.Close()
					require.True(t, rows.Next())
					var count int
					require.NoError(t, rows.Scan(&count))
					return count
				}
				outboxBefore := outboxCount()
				require.NoError(t, apply(a, true, false))
				outboxAfter := outboxCount()
				require.Equal(t, outboxBefore+1, outboxAfter)
				paused, err := repo.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.False(t, paused.Schedulable)
				require.NotEmpty(t, paused.Extra[service.BalanceAutoRestoreTokenExtraKey])
				if mode == "single manual" || mode == "stale probe" {
					require.NoError(t, repo.SetSchedulable(ctx, a.ID, false))
				}
				if mode == "bulk manual" {
					disabled := false
					_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Schedulable: &disabled})
					require.NoError(t, err)
				}
				if mode == "import manual" {
					require.NoError(t, repo.UpdateWithScheduling(ctx, paused, false))
				}
				if mode == "legacy" {
					_, err = tx.ExecContext(ctx, "UPDATE accounts SET extra = extra - 'balance_auto_restore_token' WHERE id = $1", a.ID)
					require.NoError(t, err)
				}
				if mode == "stale probe" {
					outboxBefore = outboxCount()
					require.ErrorIs(t, apply(paused, false, true), service.ErrUpstreamBillingProbeIdentityChanged)
					outboxAfter = outboxCount()
					require.Equal(t, outboxBefore, outboxAfter)
				} else {
					current, err := repo.GetByID(ctx, a.ID)
					require.NoError(t, err)
					require.NoError(t, apply(current, false, true))
				}
				got, err := repo.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, mode == "automatic", got.Schedulable)
				require.NotContains(t, got.Extra, service.BalanceAutoRestoreTokenExtraKey)
				if mode != "automatic" {
					require.NoError(t, repo.SetSchedulable(ctx, a.ID, true))
					got, err = repo.GetByID(ctx, a.ID)
					require.NoError(t, err)
					require.True(t, got.Schedulable)
					require.NotContains(t, got.Extra, service.SchedulingManualPausedAtExtraKey)
				}
			})
		}
	}
}
