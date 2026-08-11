package dto

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestGroupDynamicRateVisibility(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	source := 0.8
	change := 0.16
	group := &service.Group{
		ID:                          8,
		Name:                        "dynamic",
		RateMultiplier:              0.96,
		RateMode:                    service.GroupRateModeDynamic,
		DynamicRateMarkupPercent:    20,
		DynamicRateSourceMultiplier: &source,
		DynamicRateStatus:           service.DynamicRateStatusReady,
		DynamicRateLastDirection:    service.DynamicRateDirectionIncrease,
		DynamicRateLastChange:       &change,
		DynamicRateLastEvaluatedAt:  &now,
		DynamicRateLastAdjustedAt:   &now,
	}

	publicFields := marshalToMap(t, GroupFromService(group))
	for _, field := range []string{"rate_mode", "dynamic_rate_last_direction", "dynamic_rate_last_change", "dynamic_rate_last_adjusted_at"} {
		if _, ok := publicFields[field]; !ok {
			t.Errorf("public group DTO should include %q", field)
		}
	}
	for _, field := range []string{"dynamic_rate_markup_percent", "dynamic_rate_source_multiplier", "dynamic_rate_status", "dynamic_rate_last_evaluated_at"} {
		if _, ok := publicFields[field]; ok {
			t.Errorf("public group DTO must not include admin field %q", field)
		}
	}

	adminFields := marshalToMap(t, GroupFromServiceAdmin(group))
	for _, field := range []string{"rate_mode", "dynamic_rate_last_direction", "dynamic_rate_last_change", "dynamic_rate_last_adjusted_at", "dynamic_rate_markup_percent", "dynamic_rate_source_multiplier", "dynamic_rate_status", "dynamic_rate_last_evaluated_at"} {
		if _, ok := adminFields[field]; !ok {
			t.Errorf("admin group DTO should include %q", field)
		}
	}
}
