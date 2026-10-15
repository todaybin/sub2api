package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamBillingAutoRestoreRequiresConfirmedPositiveScheduledBalance(t *testing.T) {
	for _, tt := range []struct {
		name, mode      string
		balanceStatus   string
		balance         any
		scheduled, want bool
	}{
		{name: "positive", mode: "balance", balance: 12.0, scheduled: true, want: true},
		{name: "failed verification", mode: "balance", balance: 12.0, scheduled: true, balanceStatus: UpstreamBillingProbeStatusFailed},
		{name: "positive string", mode: "balance", balance: "0.000001", scheduled: true, want: true},
		{name: "negative", mode: "balance", balance: -12.0, scheduled: true},
		{name: "negative string", mode: "balance", balance: "-0.000001", scheduled: true},
		{name: "zero", mode: "balance", balance: 0.0, scheduled: true},
		{name: "manual", mode: "balance", balance: 12.0},
		{name: "subscription", mode: "subscription", balance: 12.0, scheduled: true},
		{name: "missing", mode: "balance", scheduled: true},
		{name: "invalid", mode: "balance", balance: "invalid", scheduled: true},
		{name: "infinite", mode: "balance", balance: math.Inf(1), scheduled: true},
		{name: "not a number", mode: "balance", balance: math.NaN(), scheduled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &UpstreamBillingProbeSnapshot{BalanceStatus: tt.balanceStatus}
			setUpstreamBillingAutoRestore(snapshot, map[string]any{"billing_mode": tt.mode, "balance": tt.balance}, tt.scheduled)
			require.Equal(t, tt.want, snapshot.AutoRestoreBalance)
		})
	}
}
