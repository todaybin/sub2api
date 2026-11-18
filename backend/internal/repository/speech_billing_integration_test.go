//go:build integration

// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestSpeechUnknownRecoveryReconciliationAndConcurrentDedup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	wallet := newUserRepositoryWithSQL(client, integrationDB)
	billing := &usageBillingRepository{db: integrationDB}
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash"})
	fixture := registerSpeechFixtureCleanup(t, user.ID)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformAnthropic, SubscriptionType: service.SubscriptionTypeStandard, AllowPointsBilling: true, PointsPerUSD: 100})
	fixture.groupID = group.ID
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Name: "speech", Key: uuid.NewString(), Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey})
	fixture.accountID = account.ID
	_, e := wallet.AdjustPoints(ctx, user.ID, 100, "add", uuid.NewString(), "speech-test")
	require.NoError(t, e)
	price := .01
	model := speech.Model{ID: "tts", Label: "语音", Provider: "aliyun", Kind: "tts", AccountID: account.ID, Groups: []int64{group.ID}, USDPerUnit: &price, Unit: "characters", Version: "v1", Region: "cn-beijing", UpstreamModel: "cosyvoice", Voices: []string{"test"}}
	for _, decision := range []string{"released", "settled"} {
		session := &service.SpeechSessionRecord{ID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, GroupID: group.ID, Model: model, RequestID: uuid.NewString(), TicketHash: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute)}
		require.NoError(t, billing.CreateSpeechSession(ctx, session))
		reservation := uuid.NewString()
		_, e = wallet.ReservePoints(ctx, reservation, user.ID, key.ID, group.ID, .2)
		require.NoError(t, e)
		meter := &service.SpeechMeterRecord{ID: session.ID + "_tts", SessionID: session.ID, ReservationID: reservation, Unit: "characters", State: "pending", SupplierState: "unknown", ProviderRequestID: speech.ProviderRequestID(session.ID)}
		require.NoError(t, billing.PutSpeechMeter(ctx, meter))
		_, e = integrationDB.ExecContext(ctx, "UPDATE points_reservations SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1", reservation)
		require.NoError(t, e)
		require.NoError(t, wallet.RecoverPoints(ctx))
		require.NoError(t, wallet.ReleasePoints(ctx, reservation))
		var reserved float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT points_reserved FROM users WHERE id=$1", user.ID).Scan(&reserved))
		require.Equal(t, 20.0, reserved)
		input := service.SpeechReconciliation{ActorID: user.ID, Decision: decision, Evidence: "provider-bill-reference"}
		if decision == "settled" {
			input.Quantity = 10
		}
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i] = billing.ReconcileSpeech(ctx, meter.ID, input) }(i)
		}
		wg.Wait()
		for _, err := range results {
			require.NoError(t, err)
		}
		require.NoError(t, billing.RecoverSpeechBilling(ctx))
		require.NoError(t, billing.RecoverSpeechBilling(ctx))
		input.Evidence = "conflicting-reference"
		require.Error(t, billing.ReconcileSpeech(ctx, meter.ID, input))
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM speech_meter_reconciliations WHERE meter_id=$1", meter.ID).Scan(&count))
		require.Equal(t, 1, count)
		var state string
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT state FROM speech_meter_segments WHERE id=$1", meter.ID).Scan(&state))
		require.Equal(t, decision, state)
	}
	current, e := wallet.GetByID(ctx, user.ID)
	require.NoError(t, e)
	require.Equal(t, 90.0, current.PointsBalance)
	require.Zero(t, current.PointsReserved)
}
func TestSpeechTicketSingleUseExpiredAndOwnership(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	billing := &usageBillingRepository{db: integrationDB}
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash"})
	fixture := registerSpeechFixtureCleanup(t, user.ID)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformAnthropic})
	fixture.groupID = group.ID
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, GroupID: &group.ID, Name: "speech", Key: uuid.NewString()})
	for _, expired := range []bool{false, true} {
		expiry := time.Now().Add(time.Minute)
		if expired {
			expiry = time.Now().Add(-time.Minute)
		}
		s := &service.SpeechSessionRecord{ID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID, GroupID: group.ID, RequestID: uuid.NewString(), TicketHash: uuid.NewString(), ExpiresAt: expiry, Language: "auto"}
		require.NoError(t, billing.CreateSpeechSession(ctx, s))
		_, e := billing.GetSpeechSession(ctx, s.ID, key.ID+1000000)
		require.Error(t, e)
		got, e := billing.RedeemSpeechSession(ctx, s.TicketHash)
		if expired {
			require.Error(t, e)
		} else {
			require.NoError(t, e)
			require.Equal(t, "auto", got.Language)
		}
		_, e = billing.RedeemSpeechSession(ctx, s.TicketHash)
		require.Error(t, e)
	}
}

// 语音结算必须提交真实事务；清理仅限本测试创建的用户及关联资源。
type speechFixtureResources struct {
	groupID   int64
	accountID int64
}

func registerSpeechFixtureCleanup(t *testing.T, userID int64) *speechFixtureResources {
	t.Helper()
	fixture := &speechFixtureResources{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err, "开始语音测试数据清理，用户 %d", userID)
		defer func() { _ = tx.Rollback() }()
		// 按外键依赖顺序清理；用量及幂等记录必须先于 API Key 删除。
		for _, query := range []string{
			"DELETE FROM speech_meter_reconciliations WHERE meter_id IN (SELECT m.id FROM speech_meter_segments m JOIN speech_sessions s ON s.id=m.session_id WHERE s.user_id=$1)",
			"DELETE FROM speech_meter_segments WHERE session_id IN (SELECT id FROM speech_sessions WHERE user_id=$1)",
			"DELETE FROM speech_sessions WHERE user_id=$1",
			"DELETE FROM usage_logs WHERE user_id=$1",
			"DELETE FROM usage_billing_dedup WHERE api_key_id IN (SELECT id FROM api_keys WHERE user_id=$1)",
			"DELETE FROM usage_billing_dedup_archive WHERE api_key_id IN (SELECT id FROM api_keys WHERE user_id=$1)",
			"DELETE FROM points_reservations WHERE user_id=$1",
			"DELETE FROM points_ledger WHERE user_id=$1",
			"DELETE FROM api_keys WHERE user_id=$1",
			"DELETE FROM users WHERE id=$1",
		} {
			_, err = tx.ExecContext(ctx, query, userID)
			require.NoError(t, err, "清理语音测试用户 %d，SQL: %s", userID, query)
		}
		if fixture.accountID != 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", fixture.accountID)
			require.NoError(t, err, "清理语音测试账号 %d", fixture.accountID)
		}
		if fixture.groupID != 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM groups WHERE id=$1", fixture.groupID)
			require.NoError(t, err, "清理语音测试分组 %d", fixture.groupID)
		}
		require.NoError(t, tx.Commit(), "提交语音测试数据清理，用户 %d", userID)
	})
	return fixture
}
