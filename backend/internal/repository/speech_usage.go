// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"time"
)

// appendSpeechUsage 与积分、唯一扣费键在同一事务落账；不保存任何正文。
func appendSpeechUsage(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	if cmd.MediaType != "audio" {
		return nil
	}
	var session, unit string
	var quantity float64
	e := tx.QueryRowContext(ctx, "SELECT session_id,unit,quantity FROM speech_meter_segments WHERE id=$1 AND reservation_id=$2 AND state='ready'", cmd.RequestID, cmd.PointsReservationID).Scan(&session, &unit, &quantity)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	mode := "speech"
	endpoint := "/v1/speech"
	group := cmd.PointsGroupID
	log := &service.UsageLog{UserID: cmd.UserID, APIKeyID: cmd.APIKeyID, AccountID: cmd.AccountID, RequestID: cmd.RequestID, Model: cmd.Model, RequestedModel: cmd.Model, GroupID: &group, BillingType: service.BillingTypePoints, TotalCost: cmd.PointsCostUSD, ActualCost: cmd.PointsCostUSD, RateMultiplier: 1, BillingMode: &mode, InboundEndpoint: &endpoint, CreatedAt: time.Now()}
	if e = execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(log)); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "UPDATE speech_meter_segments SET state='settled',updated_at=NOW() WHERE id=$1 AND state='ready'", cmd.RequestID)
	return e
}
