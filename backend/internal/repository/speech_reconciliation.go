// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
	"math"
	"strings"
)

var _ service.SpeechReconciliationStore = (*usageBillingRepository)(nil)

func (r *usageBillingRepository) ListSpeechReconciliation(ctx context.Context, cursor string) ([]map[string]any, error) {
	rows, e := r.db.QueryContext(ctx, "SELECT m.id,m.session_id,m.provider_request_id,m.quantity,m.unit,m.updated_at,s.model->>'provider',s.model->>'id' FROM speech_meter_segments m JOIN speech_sessions s ON s.id=m.session_id WHERE m.id>$1 AND m.state='pending' AND (m.supplier_state='unknown' OR m.supplier_state='inflight' AND m.updated_at<NOW()-INTERVAL '3 minutes' AND (s.model->>'kind'='tts' OR m.updated_at<NOW()-INTERVAL '125 minutes' OR EXISTS(SELECT 1 FROM points_reservations p WHERE p.id=m.reservation_id AND p.expires_at<NOW()))) ORDER BY m.id LIMIT 51", cursor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, session, request, unit, provider, model string
		var quantity float64
		var updated any
		if e = rows.Scan(&id, &session, &request, &quantity, &unit, &updated, &provider, &model); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "sessionId": session, "providerRequestId": request, "quantity": quantity, "unit": unit, "updatedAt": updated, "provider": provider, "model": model})
	}
	return out, rows.Err()
}
func (r *usageBillingRepository) ReconcileSpeech(ctx context.Context, id string, in service.SpeechReconciliation) error {
	if in.ActorID <= 0 || len(strings.TrimSpace(in.Evidence)) == 0 || len(in.Evidence) > 200 || math.IsNaN(in.Quantity) || math.IsInf(in.Quantity, 0) || in.Quantity < 0 || in.Quantity > 100000000 || in.Decision != "settled" && in.Decision != "released" || in.Decision == "released" && in.Quantity != 0 || in.Decision == "settled" && in.Quantity == 0 {
		return fmt.Errorf("SPEECH_RECONCILIATION_INVALID")
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// 锁定预扣后再锁计量行，与原账务事务保持相同锁顺序。
	var reservation string
	if e = tx.QueryRowContext(ctx, "SELECT reservation_id FROM speech_meter_segments WHERE id=$1", id).Scan(&reservation); e != nil {
		return e
	}
	var rate float64
	if reservation != "free" {
		if e = tx.QueryRowContext(ctx, "SELECT points_per_usd FROM points_reservations WHERE id=$1 FOR UPDATE", reservation).Scan(&rate); e != nil {
			return e
		}
	}
	var meter service.SpeechMeterRecord
	var supplier, state string
	var modelRaw []byte
	var key, user, group int64
	var eligible bool
	e = tx.QueryRowContext(ctx, "SELECT m.session_id,m.quantity,m.unit,m.state,m.supplier_state,s.model,s.api_key_id,s.user_id,s.group_id,(m.updated_at<NOW()-INTERVAL '3 minutes' AND (s.model->>'kind'='tts' OR m.updated_at<NOW()-INTERVAL '125 minutes' OR EXISTS(SELECT 1 FROM points_reservations p WHERE p.id=m.reservation_id AND p.expires_at<NOW()))) FROM speech_meter_segments m JOIN speech_sessions s ON s.id=m.session_id WHERE m.id=$1 FOR UPDATE OF m", id).Scan(&meter.SessionID, &meter.Quantity, &meter.Unit, &state, &supplier, &modelRaw, &key, &user, &group, &eligible)
	if e != nil {
		return e
	}
	var decision, evidence string
	var quantity float64
	e = tx.QueryRowContext(ctx, "SELECT decision,quantity,evidence FROM speech_meter_reconciliations WHERE meter_id=$1", id).Scan(&decision, &quantity, &evidence)
	if e == nil {
		if decision == in.Decision && quantity == in.Quantity && evidence == in.Evidence {
			return tx.Commit()
		}
		return fmt.Errorf("SPEECH_RECONCILIATION_CONFLICT")
	}
	if e != sql.ErrNoRows {
		return e
	}
	if state != "pending" || supplier != "unknown" && !(supplier == "inflight" && eligible) {
		return fmt.Errorf("SPEECH_RECONCILIATION_CONFLICT")
	}
	var model speech.Model
	if e = json.Unmarshal(modelRaw, &model); e != nil {
		return e
	}
	meter.ID = id
	meter.ReservationID = reservation
	meter.SupplierState = "confirmed"
	meter.Quantity = in.Quantity
	if in.Decision == "released" {
		_, e = tx.ExecContext(ctx, "UPDATE speech_meter_segments SET state='released',supplier_state='confirmed',quantity=0,points=0,updated_at=NOW() WHERE id=$1", id)
		if e == nil && reservation != "free" {
			_, e = tx.ExecContext(ctx, "WITH released AS (UPDATE points_reservations SET state='released' WHERE id=$1 AND state='active' RETURNING user_id,amount) UPDATE users u SET points_reserved=u.points_reserved-released.amount FROM released WHERE u.id=released.user_id", reservation)
		}
	} else {
		cost, err := model.Cost(in.Quantity)
		if err != nil {
			return err
		}
		if reservation == "free" {
			_, e = tx.ExecContext(ctx, "UPDATE speech_meter_segments SET state='settled',supplier_state='confirmed',quantity=$2,updated_at=NOW() WHERE id=$1", id, in.Quantity)
		} else {
			meter.Points = service.PointsCost(cost, rate)
			meter.State = "ready"
			cmd := &service.UsageBillingCommand{RequestID: id, APIKeyID: key, UserID: user, AccountID: model.AccountID, Model: model.ID, BillingType: service.BillingTypePoints, MediaType: "audio", PointsReservationID: reservation, PointsGroupID: group, PointsPerUSD: rate, PointsCostUSD: cost, APIKeyQuotaCost: cost, APIKeyRateLimitCost: cost}
			cmd.Normalize()
			if e = preparePointsSettlementOn(ctx, tx, cmd); e != nil {
				return e
			}
			body, err := json.Marshal(cmd)
			if err != nil {
				return err
			}
			// 保留原供应商请求标识，核对和待结算命令在同一事务内提交。
			_, e = tx.ExecContext(ctx, "UPDATE speech_meter_segments SET quantity=$2,points=$3,state='ready',supplier_state='confirmed',command=$4,updated_at=NOW() WHERE id=$1", id, in.Quantity, meter.Points, string(body))
		}
	}
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO speech_meter_reconciliations(meter_id,actor_id,decision,quantity,evidence) VALUES($1,$2,$3,$4,$5)", id, in.ActorID, in.Decision, in.Quantity, in.Evidence); e != nil {
		return e
	}
	return tx.Commit()
}
