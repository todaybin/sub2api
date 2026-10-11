//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMigration245BackfillsOnlyEnabledGroupsWithValidLegacyRate(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want float64
	}{
		{"normal legacy setting", "100.00000000", 100},
		{"decimal setting", "250.123456789", 250.12345679},
		{"missing setting", "", 0},
		{"invalid setting", "invalid", 0},
		{"negative setting", "-100", 0},
		{"zero setting", "0", 0},
		{"tiny setting", "0.000000001", 0},
		{"oversized setting", "1000000000000", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key='points_per_usd'`)
			require.NoError(t, err)
			if tc.raw != "" {
				_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('points_per_usd',$1)`, tc.raw)
				require.NoError(t, err)
			}
			var enabled, disabled, configured int64
			require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,allow_points_billing) VALUES ($1,true) RETURNING id`, uuid.NewString()).Scan(&enabled))
			require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,allow_points_billing) VALUES ($1,false) RETURNING id`, uuid.NewString()).Scan(&disabled))
			require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,allow_points_billing,points_per_usd) VALUES ($1,true,50) RETURNING id`, uuid.NewString()).Scan(&configured))
			migration, err := dbmigrations.FS.ReadFile("245_group_points_rate.sql")
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, string(migration))
			require.NoError(t, err)
			for id, want := range map[int64]float64{enabled: tc.want, disabled: 0, configured: 50} {
				var rate float64
				require.NoError(t, tx.QueryRowContext(ctx, `SELECT points_per_usd FROM groups WHERE id=$1`, id).Scan(&rate))
				require.Equal(t, want, rate)
			}
		})
	}
}

func TestGroupPointsRateChangeInvalidatesSingleAndSmartKeySnapshots(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	var groupID, userID int64
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups(name,allow_points_billing,points_per_usd) VALUES ($1,true,100) RETURNING id`, uuid.NewString()).Scan(&groupID))
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users(email,password_hash) VALUES ($1,'hash') RETURNING id`, uuid.NewString()+"@example.com").Scan(&userID))
	single, smart := uuid.NewString(), uuid.NewString()
	_, err := tx.ExecContext(ctx, `INSERT INTO api_keys(user_id,key,name,group_id) VALUES ($1,$2,'single',$3)`, userID, single, groupID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(user_id,key,name,routing_mode,smart_group_ids) VALUES ($1,$2,'smart','smart',jsonb_build_array($3::bigint))`, userID, smart, groupID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `DELETE FROM auth_cache_invalidation_outbox WHERE cache_key IN (encode(sha256(convert_to($1,'UTF8')),'hex'),encode(sha256(convert_to($2,'UTF8')),'hex'))`, single, smart)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET points_per_usd=250 WHERE id=$1`, groupID)
	require.NoError(t, err)
	for _, key := range []string{single, smart} {
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1,'UTF8')),'hex')`, key).Scan(&count))
		require.Positive(t, count)
	}
}
