// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/speech"
)

var _ service.SpeechStore = (*usageBillingRepository)(nil)

func (r *usageBillingRepository) CreateSpeechSession(ctx context.Context, s *service.SpeechSessionRecord) error {
	model, err := json.Marshal(s.Model)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO speech_sessions(id,api_key_id,user_id,group_id,request_id,ticket_hash,model,state,expires_at,language) VALUES($1,$2,$3,$4,$5,$6,$7,'created',$8,$9)", s.ID, s.APIKeyID, s.UserID, s.GroupID, s.RequestID, s.TicketHash, model, s.ExpiresAt, s.Language)
	return err
}
func (r *usageBillingRepository) RedeemSpeechSession(ctx context.Context, hash string) (*service.SpeechSessionRecord, error) {
	s := &service.SpeechSessionRecord{}
	var model []byte
	err := r.db.QueryRowContext(ctx, "UPDATE speech_sessions SET state='active' WHERE ticket_hash=$1 AND state='created' AND expires_at>NOW() RETURNING id,api_key_id,user_id,group_id,model,language", hash).Scan(&s.ID, &s.APIKeyID, &s.UserID, &s.GroupID, &model, &s.Language)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(model, &s.Model)
	s.State = "active"
	return s, err
}
func (r *usageBillingRepository) GetSpeechSession(ctx context.Context, id string, key int64) (*service.SpeechSessionRecord, error) {
	s := &service.SpeechSessionRecord{}
	var model []byte
	err := r.db.QueryRowContext(ctx, "SELECT id,api_key_id,user_id,group_id,model,state,language FROM speech_sessions WHERE id=$1 AND api_key_id=$2", id, key).Scan(&s.ID, &s.APIKeyID, &s.UserID, &s.GroupID, &model, &s.State, &s.Language)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(model, &s.Model)
	return s, err
}
func (r *usageBillingRepository) EndSpeechSession(ctx context.Context, id, state string) error {
	if state != "completed" && state != "interrupted" {
		return fmt.Errorf("语音会话状态无效")
	}
	_, err := r.db.ExecContext(ctx, "UPDATE speech_sessions SET state=$2 WHERE id=$1 AND state='active'", id, state)
	return err
}
func (r *usageBillingRepository) PutSpeechMeter(ctx context.Context, m *service.SpeechMeterRecord) error {
	var command any
	var db pointsSettlementSQL = r.db
	if m.Command != nil {
		m.Command.Normalize()
		value, err := json.Marshal(m.Command)
		if err != nil {
			return err
		}
		command = string(value)
	}
	if m.State == "ready" {
		if m.Command == nil || m.Command.PointsReservationID != m.ReservationID {
			return fmt.Errorf("SPEECH_METER_CONFLICT")
		}
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = preparePointsSettlementOn(ctx, tx, m.Command); err != nil {
			return err
		}
		db = tx
		if err = putSpeechMeterOn(ctx, db, m, command); err != nil {
			return err
		}
		return tx.Commit()
	}
	return putSpeechMeterOn(ctx, db, m, command)
}
func putSpeechMeterOn(ctx context.Context, db pointsSettlementSQL, m *service.SpeechMeterRecord, command any) error {
	result, err := db.ExecContext(ctx, "INSERT INTO speech_meter_segments(id,session_id,reservation_id,quantity,points,unit,state,command,supplier_state,provider_request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET quantity=excluded.quantity,points=excluded.points,state=excluded.state,supplier_state=CASE WHEN excluded.supplier_state='not_sent' THEN speech_meter_segments.supplier_state ELSE excluded.supplier_state END,provider_request_id=CASE WHEN excluded.provider_request_id='' THEN speech_meter_segments.provider_request_id ELSE excluded.provider_request_id END,command=COALESCE(excluded.command,speech_meter_segments.command),updated_at=NOW() WHERE speech_meter_segments.session_id=excluded.session_id AND speech_meter_segments.reservation_id=excluded.reservation_id AND speech_meter_segments.state NOT IN ('settled','released')", m.ID, m.SessionID, m.ReservationID, m.Quantity, m.Points, m.Unit, m.State, command, speechSupplierState(m), m.ProviderRequestID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var same bool
		err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM speech_meter_segments WHERE id=$1 AND session_id=$2 AND reservation_id=$3 AND quantity=$4 AND points=$5 AND unit=$6 AND state='settled' AND command IS NOT DISTINCT FROM $7::jsonb)", m.ID, m.SessionID, m.ReservationID, m.Quantity, m.Points, m.Unit, command).Scan(&same)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("SPEECH_METER_CONFLICT")
		}
	}
	return nil
}
func (r *usageBillingRepository) ListSpeechMeters(ctx context.Context, id string) ([]service.SpeechMeterRecord, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id,session_id,quantity,points,unit,state,supplier_state,provider_request_id FROM speech_meter_segments WHERE session_id=$1 ORDER BY id LIMIT 1000", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := []service.SpeechMeterRecord{}
	for rows.Next() {
		var m service.SpeechMeterRecord
		if err = rows.Scan(&m.ID, &m.SessionID, &m.Quantity, &m.Points, &m.Unit, &m.State, &m.SupplierState, &m.ProviderRequestID); err != nil {
			return nil, err
		}
		values = append(values, m)
	}
	return values, rows.Err()
}
func (r *usageBillingRepository) RecoverSpeechBilling(ctx context.Context) error {
	// 已过期的活动预扣仍由原积分事务结算；正文不会进入恢复任务。
	expired, err := r.db.QueryContext(ctx, "SELECT m.id,m.session_id,m.reservation_id,m.quantity,m.unit,s.api_key_id,s.user_id,s.group_id,s.model,p.points_per_usd FROM speech_meter_segments m JOIN speech_sessions s ON s.id=m.session_id JOIN points_reservations p ON p.id=m.reservation_id WHERE m.state='pending' AND m.supplier_state NOT IN ('inflight','unknown') AND p.state='active' AND p.expires_at<NOW() ORDER BY m.updated_at LIMIT 100")
	if err != nil {
		return err
	}
	type recovery struct {
		meter            service.SpeechMeterRecord
		key, user, group int64
		model            []byte
		rate             float64
	}
	items := []recovery{}
	for expired.Next() {
		var v recovery
		if err = expired.Scan(&v.meter.ID, &v.meter.SessionID, &v.meter.ReservationID, &v.meter.Quantity, &v.meter.Unit, &v.key, &v.user, &v.group, &v.model, &v.rate); err != nil {
			expired.Close()
			return err
		}
		items = append(items, v)
	}
	err = expired.Err()
	expired.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if v.meter.Quantity == 0 {
			continue
		}
		var model speech.Model
		if err = json.Unmarshal(v.model, &model); err != nil {
			return err
		}
		cost, e := model.Cost(v.meter.Quantity)
		if e != nil {
			return e
		}
		v.meter.Points = service.PointsCost(cost, v.rate)
		v.meter.State = "ready"
		v.meter.Command = &service.UsageBillingCommand{RequestID: v.meter.ID, APIKeyID: v.key, UserID: v.user, AccountID: model.AccountID, Model: model.ID, BillingType: service.BillingTypePoints, MediaType: "audio", PointsReservationID: v.meter.ReservationID, PointsGroupID: v.group, PointsPerUSD: v.rate, PointsCostUSD: cost, APIKeyQuotaCost: cost, APIKeyRateLimitCost: cost}
		if err = r.PutSpeechMeter(ctx, &v.meter); err != nil {
			return err
		}
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id,command FROM speech_meter_segments WHERE state='ready' AND command IS NOT NULL ORDER BY updated_at LIMIT 100")
	if err != nil {
		return err
	}
	type pending struct {
		id  string
		cmd service.UsageBillingCommand
	}
	all := []pending{}
	for rows.Next() {
		var item pending
		var raw []byte
		if err = rows.Scan(&item.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &item.cmd); err != nil {
			rows.Close()
			return err
		}
		all = append(all, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range all {
		if _, err = r.Apply(ctx, &item.cmd); err != nil {
			return err
		}
		if _, err = r.db.ExecContext(ctx, "UPDATE speech_meter_segments SET state='settled',updated_at=NOW() WHERE id=$1 AND state='ready'", item.id); err != nil {
			return err
		}
	}
	return nil
}

// 旧 ASR 计量调用保持默认状态，已发送音频由持久化用量保护预扣。
func speechSupplierState(m *service.SpeechMeterRecord) string {
	if m.SupplierState == "" {
		return "not_sent"
	}
	return m.SupplierState
}
