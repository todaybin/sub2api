package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Monetary usage rows retain their existing shape. The atomic points ledger is
// the source of truth for paid points and the admission-time exchange rate.
func (r *usageLogRepository) hydratePointsUsage(ctx context.Context, logs []service.UsageLog) error {
	conditions := []string{}
	args := []any{}
	indices := map[string][]int{}
	for i, l := range logs {
		if l.BillingType != service.BillingTypePoints {
			continue
		}
		args = append(args, l.RequestID, l.APIKeyID)
		conditions = append(conditions, fmt.Sprintf("(request_id=$%d AND api_key_id=$%d)", len(args)-1, len(args)))
		k := usageLogBatchKey(l.RequestID, l.APIKeyID)
		indices[k] = append(indices[k], i)
	}
	if len(conditions) == 0 {
		return nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT request_id,api_key_id,points_per_usd,-amount,cost_usd FROM points_ledger WHERE kind='usage' AND (`+strings.Join(conditions, " OR ")+`)`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var key int64
		var rate, cost, usd float64
		if err = rows.Scan(&id, &key, &rate, &cost, &usd); err != nil {
			return err
		}
		for _, i := range indices[usageLogBatchKey(id, key)] {
			logs[i].PointsPerUSD = rate
			logs[i].PointsCost = cost
			logs[i].ActualCost = usd
		}
	}
	return rows.Err()
}
