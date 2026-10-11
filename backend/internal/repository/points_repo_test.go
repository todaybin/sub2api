//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPointsRecovery_ContinuesAfterMalformedPendingCommand(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectQuery("SELECT id,state,command FROM points_reservations").WillReturnRows(sqlmock.NewRows([]string{"id", "state", "command"}).AddRow("bad", "pending", []byte(`{`)).AddRow("expired", "active", nil))
	mock.ExpectExec("WITH released AS").WithArgs("expired").WillReturnResult(sqlmock.NewResult(0, 1))
	require.Error(t, repo.RecoverPoints(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPointsSettlement_RejectsMixedPaymentBeforeWriting(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	billing := &usageBillingRepository{db: repo.sql.(*sql.DB)}
	_, err := billing.Apply(context.Background(), &service.UsageBillingCommand{RequestID: "request", PointsReservationID: "p", PointsPerUSD: 100, BillingType: service.BillingTypePoints, BalanceCost: 1})
	require.True(t, errors.Is(err, service.ErrUsageBillingRequestConflict))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPointsSettlement_OverdraftAndDollarQuotasShareOneTransaction(t *testing.T) {
	for _, failQuota := range []bool{false, true} {
		t.Run(fmt.Sprintf("quota_failure_%t", failQuota), func(t *testing.T) {
			repo, mock := newRedeemAdjustmentRepoMock(t)
			billing := &usageBillingRepository{db: repo.sql.(*sql.DB)}
			cmd := &service.UsageBillingCommand{RequestID: "request", UserID: 7, APIKeyID: 8, PointsReservationID: "payment", PointsGroupID: 9, PointsPerUSD: 100, PointsCostUSD: 1, BillingType: service.BillingTypePoints, APIKeyQuotaCost: 1, APIKeyRateLimitCost: 1}
			mock.ExpectExec("UPDATE points_reservations SET command").WithArgs(sqlmock.AnyArg(), "payment", int64(7), int64(8), int64(9), 100.0).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectBegin()
			mock.ExpectQuery("INSERT INTO usage_billing_dedup").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
			mock.ExpectQuery("SELECT user_id,api_key_id,group_id,amount,points_per_usd,state FROM points_reservations").WithArgs("payment").WillReturnRows(sqlmock.NewRows([]string{"user", "key", "group", "amount", "rate", "state"}).AddRow(7, 8, 9, 100, 100, "pending"))
			mock.ExpectQuery("UPDATE users SET points_balance").WithArgs(100.0, 100.0, int64(7)).WillReturnRows(sqlmock.NewRows([]string{"points_balance"}).AddRow(-70))
			mock.ExpectExec("INSERT INTO points_ledger").WithArgs(int64(7), "payment", -100.0, -70.0, 1.0, 100.0, "request", int64(8)).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("UPDATE points_reservations SET state='settled'").WithArgs("payment").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO auth_cache_invalidation_outbox").WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(1, 1))
			quota := mock.ExpectQuery("UPDATE api_keys.*SET quota_used").WithArgs(1.0, int64(8), service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted)
			if failQuota {
				quota.WillReturnError(errors.New("quota database failure"))
				mock.ExpectRollback()
			} else {
				quota.WillReturnRows(sqlmock.NewRows([]string{"exhausted"}).AddRow(false))
				mock.ExpectExec("UPDATE api_keys SET.*usage_5h").WithArgs(1.0, int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			result, err := billing.Apply(context.Background(), cmd)
			if failQuota {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.True(t, result.Applied)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPointsReserve_AllowsFinalRequestOverdrawAndUsesStoredRate(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT points_balance-points_reserved.*FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"available"}).AddRow(30))
	mock.ExpectQuery("SELECT allow_points_billing,points_per_usd FROM groups.*FOR SHARE").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"enabled", "rate"}).AddRow(true, 100))
	mock.ExpectExec("INSERT INTO points_reservations").WithArgs("r", int64(7), int64(8), int64(9), 100.0, 100.0).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE users SET points_reserved").WithArgs(100.0, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	rate, err := repo.ReservePoints(context.Background(), "r", 7, 8, 9, 1)
	require.NoError(t, err)
	require.Equal(t, 100.0, rate)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPointsReserve_NoAvailableCreditDoesNotCreateReservation(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT points_balance-points_reserved.*FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"available"}).AddRow(-70))
	mock.ExpectRollback()
	_, err := repo.ReservePoints(context.Background(), "r", 7, 8, 9, 1)
	require.ErrorIs(t, err, service.ErrInsufficientPoints)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPointsReserve_GroupConfigurationIsAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		rate    float64
		wantErr error
	}{
		{"disabled never reserves", false, 100, service.ErrPointsBillingDisabled},
		{"missing group rate rejects", true, 0, service.ErrInvalidPointsRate},
		{"invalid group rate rejects", true, -1, service.ErrInvalidPointsRate},
		{"group one uses 100", true, 100, nil},
		{"group two uses 250", true, 250, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mock := newRedeemAdjustmentRepoMock(t)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT points_balance-points_reserved.*FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"available"}).AddRow(30))
			mock.ExpectQuery("SELECT allow_points_billing,points_per_usd FROM groups.*FOR SHARE").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"enabled", "rate"}).AddRow(tc.enabled, tc.rate))
			if tc.wantErr != nil {
				mock.ExpectRollback()
			} else {
				points := service.PointsCost(0.02, tc.rate)
				mock.ExpectExec("INSERT INTO points_reservations").WithArgs("r", int64(7), int64(8), int64(9), points, tc.rate).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec("UPDATE users SET points_reserved").WithArgs(points, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			rate, err := repo.ReservePoints(context.Background(), "r", 7, 8, 9, 0.02)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.rate, rate)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPointsAdjustment_TopUpRepaysDebtAtomically(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT points_balance,points_reserved.*FOR UPDATE").WillReturnRows(sqlmock.NewRows([]string{"balance", "reserved"}).AddRow(-70, 0))
	mock.ExpectQuery("SELECT amount,balance_after,notes FROM points_ledger").WillReturnRows(sqlmock.NewRows([]string{"amount", "balance_after", "notes"}))
	mock.ExpectExec("UPDATE users SET points_balance").WithArgs(30.0, int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO points_ledger").WithArgs(int64(7), "topup", "adjustment", 100.0, 30.0, "test").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	change, err := repo.AdjustPoints(context.Background(), 7, 100, "add", "topup", "test")
	require.NoError(t, err)
	require.Equal(t, -70.0, change.Old)
	require.Equal(t, 30.0, change.New)
	require.NoError(t, mock.ExpectationsWereMet())
}
