package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.PointsRepository = (*userRepository)(nil)

func (r *userRepository) PointsSummary(ctx context.Context, userID int64) (*service.PointsSummary, error) {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT COALESCE(SUM(-amount),0),COALESCE(SUM(cost_usd),0),
 (SELECT COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE user_id=$1 AND billing_type=0)
 FROM points_ledger WHERE user_id=$1 AND kind='usage'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result service.PointsSummary
	if rows.Next() {
		err = rows.Scan(&result.PointsSpent, &result.PointsCostUSD, &result.MoneySpentUSD)
	}
	if err != nil {
		return nil, err
	}
	return &result, rows.Err()
}

func (r *userRepository) PointsAccount(ctx context.Context, userID int64) (*service.PointsAccountInfo, error) {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT points_balance,points_reserved FROM users WHERE id=$1 AND deleted_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, service.ErrUserNotFound
	}
	var info service.PointsAccountInfo
	if err = rows.Scan(&info.Balance, &info.Reserved); err != nil {
		return nil, err
	}
	return &info, rows.Err()
}

func (r *userRepository) PointsAvailable(ctx context.Context, userID int64) (float64, error) {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT points_balance - points_reserved FROM users WHERE id=$1 AND deleted_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, service.ErrUserNotFound
	}
	var amount float64
	if err = rows.Scan(&amount); err != nil {
		return 0, err
	}
	return amount, rows.Err()
}

func (r *userRepository) ReservePoints(ctx context.Context, id string, userID, keyID, groupID int64, estimate float64) (float64, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	exec := sqlExecutorFromEntClient(tx.Client())
	if exec == nil {
		return 0, errors.New("points transaction executor unavailable")
	}
	rows, err := exec.QueryContext(ctx, `SELECT points_balance-points_reserved FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, userID)
	if err != nil {
		return 0, err
	}
	var available float64
	if !rows.Next() {
		rows.Close()
		return 0, service.ErrUserNotFound
	}
	err = rows.Scan(&available)
	rows.Close()
	if err != nil {
		return 0, err
	}
	if available <= 0 {
		return 0, service.ErrInsufficientPoints
	}
	// A share lock gives this reservation a stable group configuration while
	// administrators update the switch or rate. Settlement uses this snapshot.
	rows, err = exec.QueryContext(ctx, `SELECT allow_points_billing,points_per_usd FROM groups WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, groupID)
	if err != nil {
		return 0, err
	}
	var enabled bool
	var rate float64
	if !rows.Next() {
		rows.Close()
		return 0, service.ErrGroupNotFound
	}
	err = rows.Scan(&enabled, &rate)
	rows.Close()
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, service.ErrPointsBillingDisabled
	}
	if err := service.ValidateGroupPointsRate(true, rate); err != nil {
		return 0, err
	}
	rate = service.QuantizeUsageBillingAmount(rate)
	if rate <= 0 {
		return 0, service.ErrBillingServiceUnavailable
	}
	amount := service.PointsCost(estimate, rate)
	// Unknown/free estimates still reserve the remaining positive credit. Admission
	// permits the final request to overdraw; later concurrent requests use money.
	if amount <= 0 {
		amount = available
	}
	_, err = exec.ExecContext(ctx, `INSERT INTO points_reservations(id,user_id,api_key_id,group_id,amount,points_per_usd,expires_at) VALUES($1,$2,$3,$4,$5,$6,NOW()+INTERVAL '15 minutes')`, id, userID, keyID, groupID, amount, rate)
	if err != nil {
		return 0, err
	}
	_, err = exec.ExecContext(ctx, `UPDATE users SET points_reserved=points_reserved+$1 WHERE id=$2`, amount, userID)
	if err != nil {
		return 0, err
	}
	return rate, tx.Commit()
}

func (r *userRepository) RenewPoints(ctx context.Context, id string) error {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `UPDATE points_reservations SET expires_at=NOW()+INTERVAL '15 minutes' WHERE id=$1 AND state='active'`, id)
	return err
}

func (r *userRepository) ReleasePoints(ctx context.Context, id string) error {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `WITH released AS (
 UPDATE points_reservations SET state='released' WHERE id=$1 AND state='active' RETURNING user_id,amount
 ) UPDATE users u SET points_reserved=u.points_reserved-released.amount FROM released WHERE u.id=released.user_id`, id)
	return err
}

func (r *userRepository) RecoverPoints(ctx context.Context) error {
	return r.RecoverPointsWithEffects(ctx, nil)
}

func (r *userRepository) RecoverPointsWithEffects(ctx context.Context, afterCommit func(*service.UsageBillingCommand)) error {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id,state,command FROM points_reservations WHERE state='pending' OR (state='active' AND expires_at<NOW()) ORDER BY created_at LIMIT 100`)
	if err != nil {
		return err
	}
	type entry struct {
		id, state string
		command   []byte
	}
	var pending []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.state, &e.command); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	billing := &usageBillingRepository{db: db}
	var recoveryErrors []error
	for _, e := range pending {
		if e.state == "active" {
			// Recheck the lease in SQL: a live handler may have renewed after our read.
			_, err = db.ExecContext(ctx, `WITH released AS (UPDATE points_reservations SET state='released' WHERE id=$1 AND state='active' AND expires_at<NOW() RETURNING user_id,amount) UPDATE users u SET points_reserved=u.points_reserved-released.amount FROM released WHERE u.id=released.user_id`, e.id)
		} else {
			var cmd service.UsageBillingCommand
			if err = json.Unmarshal(e.command, &cmd); err == nil {
				var result *service.UsageBillingApplyResult
				result, err = billing.Apply(ctx, &cmd)
				if err == nil && result != nil && result.Applied && afterCommit != nil {
					afterCommit(&cmd)
				}
			}
		}
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("reservation %s: %w", e.id, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

func (r *userRepository) AdjustPoints(ctx context.Context, userID int64, value float64, operation, id, notes string) (service.BalanceChange, error) {
	var change service.BalanceChange
	if value < 0 || value > 999999999999 || math.IsNaN(value) || math.IsInf(value, 0) {
		return change, service.ErrInvalidPointsAmount
	}
	if operation != "set" && operation != "add" && operation != "subtract" {
		return change, fmt.Errorf("invalid points operation")
	}
	var owned *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		owned, err = r.client.Tx(ctx)
		if err != nil {
			return change, err
		}
		defer owned.Rollback()
		ctx = dbent.NewTxContext(ctx, owned)
	}
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return change, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT points_balance,points_reserved FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, userID)
	if err != nil {
		return change, err
	}
	var reserved float64
	if !rows.Next() {
		rows.Close()
		return change, service.ErrUserNotFound
	}
	err = rows.Scan(&change.Old, &reserved)
	rows.Close()
	if err != nil {
		return change, err
	}
	rows, err = exec.QueryContext(ctx, `SELECT amount,balance_after,notes FROM points_ledger WHERE operation_id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return change, err
	}
	if rows.Next() {
		var delta float64
		var originalNotes string
		err = rows.Scan(&delta, &change.New, &originalNotes)
		rows.Close()
		if err != nil {
			return change, err
		}
		expected := service.QuantizeUsageBillingAmount(value)
		if operation == "subtract" {
			expected = -expected
		}
		if originalNotes != notes || (operation == "set" && change.New != expected) || (operation != "set" && delta != expected) {
			return change, service.ErrPointsOperationConflict
		}
		change.Old = change.New - delta
		return change, err
	}
	rows.Close()
	value = service.QuantizeUsageBillingAmount(value)
	switch operation {
	case "set":
		change.New = value
	case "add":
		change.New = service.QuantizeUsageBillingAmount(change.Old + value)
	case "subtract":
		change.New = service.QuantizeUsageBillingAmount(change.Old - value)
	}
	if operation != "add" && reserved > 0 {
		return change, service.ErrPointsReserved
	}
	if operation != "add" && change.New < 0 {
		return change, service.ErrBalanceNegative
	}
	_, err = exec.ExecContext(ctx, `UPDATE users SET points_balance=$1,updated_at=NOW() WHERE id=$2`, change.New, userID)
	if err != nil {
		return change, err
	}
	_, err = exec.ExecContext(ctx, `INSERT INTO points_ledger(user_id,operation_id,kind,amount,balance_after,notes) VALUES($1,$2,$3,$4,$5,$6)`, userID, id, "adjustment", service.QuantizeUsageBillingAmount(change.New-change.Old), change.New, notes)
	if err != nil {
		return change, err
	}
	if owned != nil {
		err = owned.Commit()
	}
	return change, err
}

func (r *userRepository) ListPointsLedger(ctx context.Context, userID int64, page, size int) ([]service.PointsLedgerEntry, int64, error) {
	exec, err := r.userProfileIdentitySQL(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT COUNT(*) FROM points_ledger WHERE user_id=$1`, userID)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if rows.Next() {
		err = rows.Scan(&total)
	}
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	rows, err = exec.QueryContext(ctx, `SELECT id,amount,balance_after,cost_usd,points_per_usd,kind,notes,request_id,created_at FROM points_ledger WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, userID, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := []service.PointsLedgerEntry{}
	for rows.Next() {
		var e service.PointsLedgerEntry
		if err = rows.Scan(&e.ID, &e.Amount, &e.BalanceAfter, &e.CostUSD, &e.PointsPerUSD, &e.Kind, &e.Notes, &e.RequestID, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func (r *usageBillingRepository) preparePointsSettlement(ctx context.Context, cmd *service.UsageBillingCommand) error {
	if cmd.BalanceCost != 0 || cmd.SubscriptionCost != 0 || cmd.BillingType != service.BillingTypePoints || cmd.PointsCostUSD < 0 || math.IsNaN(cmd.PointsCostUSD) || math.IsInf(cmd.PointsCostUSD, 0) || cmd.PointsPerUSD <= 0 || math.IsNaN(cmd.PointsPerUSD) || math.IsInf(cmd.PointsPerUSD, 0) {
		return service.ErrUsageBillingRequestConflict
	}
	body, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE points_reservations SET command=$1,state='pending' WHERE id=$2 AND user_id=$3 AND api_key_id=$4 AND group_id=$5 AND points_per_usd=$6 AND (state='active' OR (state='pending' AND command=$1::jsonb))`, string(body), cmd.PointsReservationID, cmd.UserID, cmd.APIKeyID, cmd.PointsGroupID, cmd.PointsPerUSD)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var state string
		err = r.db.QueryRowContext(ctx, `SELECT state FROM points_reservations WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, cmd.PointsReservationID, cmd.UserID, cmd.APIKeyID).Scan(&state)
		if err != nil {
			return err
		}
		if state != "settled" {
			return service.ErrUsageBillingRequestConflict
		}
	}
	return nil
}

func settleUsagePoints(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var userID, keyID, groupID int64
	var amount, rate float64
	var state string
	err := tx.QueryRowContext(ctx, `SELECT user_id,api_key_id,group_id,amount,points_per_usd,state FROM points_reservations WHERE id=$1 FOR UPDATE`, cmd.PointsReservationID).Scan(&userID, &keyID, &groupID, &amount, &rate, &state)
	if err != nil {
		return err
	}
	if state != "pending" || userID != cmd.UserID || keyID != cmd.APIKeyID || groupID != cmd.PointsGroupID || rate != cmd.PointsPerUSD {
		return service.ErrUsageBillingRequestConflict
	}
	points := service.PointsCost(cmd.PointsCostUSD, rate)
	var balance float64
	err = tx.QueryRowContext(ctx, `UPDATE users SET points_balance=points_balance-$1,points_reserved=points_reserved-$2,updated_at=NOW() WHERE id=$3 RETURNING points_balance`, points, amount, userID).Scan(&balance)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO points_ledger(user_id,operation_id,kind,amount,balance_after,cost_usd,points_per_usd,request_id,api_key_id) VALUES($1,$2,'usage',$3,$4,$5,$6,$7,$8)`, userID, cmd.PointsReservationID, -points, balance, cmd.PointsCostUSD, rate, cmd.RequestID, keyID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE points_reservations SET state='settled' WHERE id=$1`, cmd.PointsReservationID)
	if err != nil {
		return err
	}
	// Durable invalidation also covers recovery, when no handler is left to clear
	// an exhausted key's auth snapshot after the committed quota increment.
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_cache_invalidation_outbox(cache_key) SELECT encode(sha256(convert_to(key,'UTF8')),'hex') FROM api_keys WHERE id=$1 AND deleted_at IS NULL AND key<>''`, keyID)
	return err
}
