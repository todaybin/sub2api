//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPointsCost_UsesFinalMonetaryCostAndDecimalPrecision(t *testing.T) {
	require.Equal(t, 100.0, PointsCost(1, 100))
	require.Equal(t, 0.025, PointsCost(0.000025, 1000))
	require.Equal(t, 0.00000003, PointsCost(0.00000001, 2.5))
	require.Equal(t, 0.0, PointsCost(0, 100))
}

func TestPointsBillingCommand_PreservesMonetaryQuotasWithoutDebitingMoney(t *testing.T) {
	p := &postUsageBillingParams{
		Cost: &CostBreakdown{TotalCost: 0.5, ActualCost: 1.25},
		User: &User{ID: 7}, APIKey: &APIKey{ID: 8, Quota: 10, RateLimit5h: 10},
		Account: &Account{ID: 9}, APIKeyService: &stubAPIKeyQuotaUpdater{},
		PointsPayment: &PointsPayment{ID: "payment", GroupID: 4, Rate: 100},
	}
	cmd := buildUsageBillingCommand("request", &UsageLog{}, p)
	require.Equal(t, 0.0, cmd.BalanceCost)
	require.Equal(t, 0.0, cmd.SubscriptionCost)
	require.Equal(t, 1.25, cmd.APIKeyQuotaCost)
	require.Equal(t, 1.25, cmd.APIKeyRateLimitCost)
	require.Equal(t, 1.25, cmd.PointsCostUSD)
	require.Equal(t, BillingTypePoints, cmd.BillingType)
	require.Equal(t, 125.0, PointsCost(cmd.PointsCostUSD, cmd.PointsPerUSD))
}

type stubAPIKeyQuotaUpdater struct{}

func (*stubAPIKeyQuotaUpdater) UpdateQuotaUsed(context.Context, int64, float64) error { return nil }
func (*stubAPIKeyQuotaUpdater) UpdateRateLimitUsage(context.Context, int64, float64) error {
	return nil
}

func TestPointsFingerprint_IgnoresReservationAttemptButIncludesRate(t *testing.T) {
	a := &UsageBillingCommand{RequestID: "same", APIKeyID: 7, PointsReservationID: "first", PointsGroupID: 3, PointsPerUSD: 100, PointsCostUSD: 1}
	b := *a
	b.PointsReservationID = "retry"
	a.Normalize()
	b.Normalize()
	require.Equal(t, a.RequestFingerprint, b.RequestFingerprint)
	b.PointsPerUSD = 200
	b.RequestFingerprint = ""
	b.Normalize()
	require.NotEqual(t, a.RequestFingerprint, b.RequestFingerprint)
}
