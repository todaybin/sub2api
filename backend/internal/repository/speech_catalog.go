// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageBillingRepository) CompareSpeechModels(ctx context.Context, before, after string) error {
	var result sql.Result
	var err error
	if before == "" {
		result, err = r.db.ExecContext(ctx, "INSERT INTO settings(key,value,updated_at) VALUES('workmesh_speech_models_v1',$1,NOW()) ON CONFLICT(key) DO NOTHING", after)
	}
	// 非首次配置走条件更新，零行代表另一管理员已保存。
	if before != "" {
		result, err = r.db.ExecContext(ctx, "UPDATE settings SET value=$2,updated_at=NOW() WHERE key='workmesh_speech_models_v1' AND value=$1", before, after)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("SPEECH_CONFIG_CONFLICT")
	}
	return nil
}
func (r *usageBillingRepository) ListUserSpeech(ctx context.Context, user int64, cursor string) ([]map[string]any, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT s.id,s.model->>'id',s.model->>'kind',s.model->>'version',s.state,s.created_at,COALESCE(SUM(m.quantity),0),COALESCE(SUM(CASE WHEN m.state='settled' THEN m.points ELSE 0 END),0),COUNT(m.id) FILTER(WHERE m.state IN ('pending','ready')),COUNT(m.id) FILTER(WHERE m.supplier_state='unknown' OR m.supplier_state='inflight' AND m.updated_at<NOW()-INTERVAL '3 minutes'),COUNT(m.id) FILTER(WHERE m.state='settled'),COUNT(m.id) FILTER(WHERE m.state='released') FROM speech_sessions s LEFT JOIN speech_meter_segments m ON m.session_id=s.id WHERE s.user_id=$1 AND s.id>$2 GROUP BY s.id ORDER BY s.id LIMIT 51", user, cursor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []map[string]any{}
	for rows.Next() {
		var id, model, kind, version, state string
		var created any
		var quantity, points float64
		var pending, uncertain, settled, released int
		if err = rows.Scan(&id, &model, &kind, &version, &state, &created, &quantity, &points, &pending, &uncertain, &settled, &released); err != nil {
			return nil, err
		}
		billingState := "processing"
		if uncertain > 0 {
			billingState = "uncertain"
		} else if pending > 0 {
			billingState = "pending"
		} else if settled > 0 {
			billingState = "settled"
		} else if released > 0 {
			billingState = "released"
		}
		out = append(out, map[string]any{"id": id, "model": model, "feature": kind, "priceVersion": version, "state": state, "createdAt": created, "quantity": quantity, "points": points, "pending": pending, "uncertain": uncertain, "billingState": billingState})
	}
	return out, rows.Err()
}

var _ service.SpeechStore = (*usageBillingRepository)(nil)
