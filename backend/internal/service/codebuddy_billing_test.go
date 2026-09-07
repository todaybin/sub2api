//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeBuddyTokenCostBreakdownUsesCreditToTokenCalibration(t *testing.T) {
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 500}
	cost := CodeBuddyTokenCostBreakdown(usage, 0.79)

	expectedRate := 0.79 * 70 / (2000 * 31874)
	require.InDelta(t, 1000*expectedRate, cost.InputCost, 1e-18)
	require.InDelta(t, 500*expectedRate, cost.OutputCost, 1e-18)
	require.InDelta(t, 1500*expectedRate, cost.TotalCost, 1e-18)
	require.InDelta(t, 1500*expectedRate, cost.ActualCost, 1e-18)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

func TestCodeBuddyBillingDetailsApplyFixedAndDynamicGroupRules(t *testing.T) {
	multiplier := 0.79
	account := Account{
		Platform: PlatformCodeBuddy,
		Extra: map[string]any{
			"codebuddy_model_catalog": []any{
				map[string]any{"id": "hy3", "credits_multiplier": multiplier},
			},
		},
	}

	fixed := CodeBuddyBillingDetailsForGroup(
		&Group{Platform: PlatformCodeBuddy, RateMode: GroupRateModeFixed, RateMultiplier: 1.25},
		[]Account{account},
	)
	require.Len(t, fixed.Models, 1)
	require.InDelta(t, 1.25, fixed.Models[0].FinalMultiplier, 1e-12)

	dynamic := CodeBuddyBillingDetailsForGroup(
		&Group{Platform: PlatformCodeBuddy, RateMode: GroupRateModeDynamic, DynamicRateMarkupPercent: 20},
		[]Account{account},
	)
	require.Len(t, dynamic.Models, 1)
	require.InDelta(t, 0.2, dynamic.DynamicProfitRate, 1e-12)
	require.InDelta(t, multiplier*1.2, dynamic.Models[0].FinalMultiplier, 1e-12)
	require.InDelta(t, dynamic.Models[0].UpstreamCostPerToken*1.2, dynamic.Models[0].FinalCostPerToken, 1e-18)
}
