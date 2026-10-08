package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func schedulingRevisionJSON(account *service.Account) string {
	value, _ := json.Marshal(account.Extra[service.SchedulingRevisionExtraKey])
	return string(value)
}

func accountExtraValueJSON(account *service.Account, key string) string {
	value, _ := json.Marshal(account.Extra[key])
	return string(value)
}

var balanceSchedulingManagedKeys = []string{
	service.BalanceAutoRestoreTokenExtraKey, service.SchedulingRevisionExtraKey,
	service.SchedulingManualPausedAtExtraKey, service.UpstreamBillingBalanceAutoDisabledAtExtraKey,
}

func stripBalanceSchedulingExtra(updates map[string]any) map[string]any {
	updates = copyJSONMap(updates)
	for _, key := range balanceSchedulingManagedKeys {
		delete(updates, key)
	}
	if raw, ok := updates[service.CodeBuddyCheckinExtraKey]; ok {
		data, err := json.Marshal(raw)
		var settings map[string]any
		if err == nil && json.Unmarshal(data, &settings) == nil && settings != nil {
			delete(settings, "auto_disabled_by_balance")
			updates[service.CodeBuddyCheckinExtraKey] = settings
		}
	}
	return updates
}

func preserveBalanceSchedulingExtra(extra, current map[string]any) {
	for _, key := range balanceSchedulingManagedKeys {
		delete(extra, key)
		if value, exists := current[key]; exists {
			extra[key] = value
		}
	}
	// A stale edit must not recreate a cleared CodeBuddy automatic marker.
	if raw, exists := extra[service.CodeBuddyCheckinExtraKey]; exists {
		data, _ := json.Marshal(raw)
		var settings map[string]any
		if json.Unmarshal(data, &settings) == nil && settings != nil {
			delete(settings, "auto_disabled_by_balance")
			currentSettings := service.CodeBuddyCheckinSettingsForAccount(&service.Account{Extra: current})
			if currentSettings.AutoDisabledByBalance {
				settings["auto_disabled_by_balance"] = true
			}
			extra[service.CodeBuddyCheckinExtraKey] = settings
		}
	}
}

// Every manual override changes the revision, including false -> false, so
// an in-flight probe cannot undo it even when the boolean has not changed.
func manualSchedulingExtraSQL(base, enabled string) string {
	return "((" + base + ") - 'balance_auto_restore_token' - 'upstream_billing_balance_auto_disabled_at' - 'scheduling_manual_paused_at')" +
		" || jsonb_build_object('scheduling_revision', '" + uuid.NewString() + "')" +
		" || CASE WHEN " + enabled + " THEN '{}'::jsonb ELSE jsonb_build_object('scheduling_manual_paused_at', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')) END" +
		" || CASE WHEN jsonb_typeof((" + base + ") -> 'codebuddy_checkin') = 'object' THEN jsonb_build_object('codebuddy_checkin', ((" + base + ") -> 'codebuddy_checkin') - 'auto_disabled_by_balance') ELSE '{}'::jsonb END"
}

func (r *accountRepository) UpdateCodeBuddyBalanceScheduling(ctx context.Context, expected *service.Account, settings service.CodeBuddyCheckinSettings, disable, restore bool) error {
	if expected == nil || expected.Platform != service.PlatformCodeBuddy || expected.Type != service.AccountTypeOAuth {
		return service.ErrUpstreamBillingProbeAccountInvalid
	}
	settings.AutoDisabledByBalance = false // SQL derives this from current state.
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	credentials, err := json.Marshal(expected.Credentials)
	if err != nil {
		return err
	}
	contextTx := dbent.TxFromContext(ctx)
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if contextTx == nil {
		tx, err = r.client.Tx(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		ctx = dbent.NewTxContext(ctx, tx)
		client = tx.Client()
	}
	proxyMatches, err := lockAndMatchProbeProxyIdentity(ctx, client, expected)
	if err != nil {
		return err
	}
	if !proxyMatches {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	var proxyID any
	if expected.ProxyID != nil {
		proxyID = *expected.ProxyID
	}
	result, err := client.ExecContext(ctx, `
		UPDATE accounts SET
		 extra = (COALESCE(extra, '{}'::jsonb) - CASE WHEN $7 AND schedulable IS FALSE
		   AND (jsonb_typeof(extra -> 'balance_auto_restore_token') = 'string' AND extra ->> 'balance_auto_restore_token' <> '')
		   AND NOT (extra ? 'scheduling_manual_paused_at')
		   THEN ARRAY['balance_auto_restore_token', 'upstream_billing_balance_auto_disabled_at'] ELSE ARRAY[]::text[] END)
		   || jsonb_build_object('codebuddy_checkin', $2::jsonb || jsonb_build_object('auto_disabled_by_balance', CASE
		     WHEN $6 AND schedulable IS TRUE AND NOT (extra ? 'scheduling_manual_paused_at') THEN TRUE
		     WHEN $7 AND (jsonb_typeof(extra -> 'balance_auto_restore_token') = 'string' AND extra ->> 'balance_auto_restore_token' <> '') AND NOT (extra ? 'scheduling_manual_paused_at') THEN FALSE
		     ELSE COALESCE(extra #> '{codebuddy_checkin,auto_disabled_by_balance}', 'false'::jsonb) = 'true'::jsonb END))
		   || CASE WHEN $6 AND schedulable IS TRUE AND NOT (extra ? 'scheduling_manual_paused_at')
		     THEN jsonb_build_object('balance_auto_restore_token', $8::text, 'upstream_billing_balance_auto_disabled_at', $9::text) ELSE '{}'::jsonb END,
		 schedulable = CASE
		   WHEN $6 AND schedulable IS TRUE AND NOT (extra ? 'scheduling_manual_paused_at') THEN FALSE
		   WHEN $7 AND schedulable IS FALSE AND (jsonb_typeof(extra -> 'balance_auto_restore_token') = 'string' AND extra ->> 'balance_auto_restore_token' <> '') AND NOT (extra ? 'scheduling_manual_paused_at') THEN TRUE
		   ELSE schedulable END, updated_at = NOW()
		WHERE id = $1 AND platform = 'codebuddy' AND type = 'oauth' AND status = 'active'
		 AND credentials = $3::jsonb AND COALESCE(extra -> 'scheduling_revision', 'null'::jsonb) = $4::jsonb
		 AND proxy_id IS NOT DISTINCT FROM $5 AND deleted_at IS NULL
		 AND schedulable = $10 AND COALESCE(extra -> 'balance_auto_restore_token', 'null'::jsonb) = $11::jsonb
		 AND COALESCE(extra -> 'codebuddy_checkin', 'null'::jsonb) = $12::jsonb
	`, expected.ID, string(data), string(credentials), schedulingRevisionJSON(expected), proxyID, disable, restore, uuid.NewString(), time.Now().UTC().Format(time.RFC3339), expected.Schedulable, accountExtraValueJSON(expected, service.BalanceAutoRestoreTokenExtraKey), accountExtraValueJSON(expected, service.CodeBuddyCheckinExtraKey))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &expected.ID, nil, nil); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
		r.syncSchedulerAccountSnapshot(ctx, expected.ID)
	}
	return nil
}

func (r *accountRepository) setManualSchedulable(ctx context.Context, id int64, enabled bool) error {
	client := clientFromContext(ctx, r.client)
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}
	result, err := client.ExecContext(ctx, "UPDATE accounts SET schedulable = $2, extra = "+manualSchedulingExtraSQL("COALESCE(extra, '{}'::jsonb)", "$2::boolean")+", updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL", id, enabled)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}
