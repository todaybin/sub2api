package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodeBuddyBalanceSchedulingCASAndOutbox(t *testing.T) {
	for _, tt := range []struct {
		name      string
		affected  int64
		outboxErr error
	}{
		{name: "commit", affected: 1},
		{name: "manual pause during probe", affected: 0},
		{name: "outbox failure", affected: 1, outboxErr: errors.New("outbox failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			mock.ExpectExec(`(?s)UPDATE accounts.*balance_auto_restore_token.*scheduling_manual_paused_at.*credentials = \$3.*scheduling_revision.*schedulable = \$10.*codebuddy_checkin.*\$12`).
				WithArgs(int64(17), sqlmock.AnyArg(), `{"access_token":"token"}`, `"r1"`, nil, false, true, sqlmock.AnyArg(), sqlmock.AnyArg(), false, `"automatic"`, "null").
				WillReturnResult(sqlmock.NewResult(0, tt.affected))
			if tt.affected > 0 {
				expectation := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox"))
				if tt.outboxErr != nil {
					expectation.WillReturnError(tt.outboxErr)
				} else {
					expectation.WillReturnResult(sqlmock.NewResult(1, 1))
				}
			}
			if tt.affected == 0 || tt.outboxErr != nil {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			repo := newAccountRepositoryWithSQL(client, db, nil)
			err = repo.UpdateCodeBuddyBalanceScheduling(context.Background(), &service.Account{
				ID: 17, Platform: service.PlatformCodeBuddy, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "token"}, Extra: map[string]any{service.SchedulingRevisionExtraKey: "r1", service.BalanceAutoRestoreTokenExtraKey: "automatic"},
			}, service.CodeBuddyCheckinSettings{LastCreditRemaining: 5}, false, true)
			if tt.affected == 0 {
				require.ErrorIs(t, err, service.ErrUpstreamBillingProbeIdentityChanged)
			} else if tt.outboxErr != nil {
				require.ErrorIs(t, err, tt.outboxErr)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestManualPauseOutboxFailureRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts.*balance_auto_restore_token.*scheduling_revision.*scheduling_manual_paused_at`).WithArgs(int64(17), false).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO scheduler_outbox").WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()
	repo := newAccountRepositoryWithSQL(client, db, nil)
	require.EqualError(t, repo.SetSchedulable(context.Background(), 17, false), "outbox failed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStaleAccountExtraCannotReinstateAutomaticRecovery(t *testing.T) {
	stale := map[string]any{service.BalanceAutoRestoreTokenExtraKey: "old", service.UpstreamBillingBalanceAutoDisabledAtExtraKey: "old", service.CodeBuddyCheckinExtraKey: service.CodeBuddyCheckinSettings{Enabled: true, AutoDisabledByBalance: true}}
	current := map[string]any{service.SchedulingRevisionExtraKey: "manual", service.SchedulingManualPausedAtExtraKey: "now", service.CodeBuddyCheckinExtraKey: service.CodeBuddyCheckinSettings{Enabled: true}}
	preserveBalanceSchedulingExtra(stale, current)
	require.NotContains(t, stale, service.BalanceAutoRestoreTokenExtraKey)
	require.NotContains(t, stale, service.UpstreamBillingBalanceAutoDisabledAtExtraKey)
	require.Equal(t, "manual", stale[service.SchedulingRevisionExtraKey])
	require.False(t, service.CodeBuddyCheckinSettingsForAccount(&service.Account{Extra: stale}).AutoDisabledByBalance)
}

func TestStaleAccountEditPreservesManualPause(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectQuery(`(?s)SELECT.*schedulable.*FOR NO KEY UPDATE`).
		WithArgs(int64(27), service.PlatformOpenAI, service.AccountTypeAPIKey, `{"api_key":"test"}`, nil).
		WillReturnRows(sqlmock.NewRows([]string{"identity", "ollama_group", "proxy", "enabled", "sync", "snapshot", "session", "auto", "usage", "opencode_group", "opencode_auto", "opencode_usage", "extra", "schedulable"}).
			AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, []byte(`{"scheduling_revision":"manual","scheduling_manual_paused_at":"now"}`), false))
	account := &service.Account{ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Schedulable: true,
		Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{service.BalanceAutoRestoreTokenExtraKey: "old"}}
	extra, err := lockAndMergeAccountProbeExtra(context.Background(), client, account, nil, nil)
	require.NoError(t, err)
	require.False(t, account.Schedulable)
	require.Equal(t, "manual", extra[service.SchedulingRevisionExtraKey])
	require.Contains(t, extra, service.SchedulingManualPausedAtExtraKey)
	require.NotContains(t, extra, service.BalanceAutoRestoreTokenExtraKey)
	require.NoError(t, mock.ExpectationsWereMet())
}
