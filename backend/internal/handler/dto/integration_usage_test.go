//go:build unit

package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIntegrationUsageDoesNotExposeUpstreamFields(t *testing.T) {
	upstream := "private-provider"
	cost := 123.0
	log := &service.UsageLog{ID: 1, UserID: 2, APIKeyID: 3, AccountID: 4, Model: upstream, RequestedModel: "public-model",
		PointsCost: 12.5, PointsPerUSD: 100, BillingType: service.BillingTypePoints,
		UpstreamEndpoint: &upstream, UpstreamModel: &upstream, UpstreamRequestID: &upstream,
		ModelMappingChain: &upstream, AccountStatsCost: &cost, Account: &service.Account{Name: upstream}}
	data, err := json.Marshal(IntegrationUsageLogFromService(log))
	require.NoError(t, err)
	require.Contains(t, string(data), `"model":"public-model"`)
	require.Contains(t, string(data), `"points_cost":12.5`)
	for _, forbidden := range []string{upstream, "upstream", "account", "channel", "mapping"} {
		require.NotContains(t, string(data), forbidden)
	}
	stats := &usagestats.UsageStats{TotalRequests: 2, TotalPointsCost: 12.5, TotalAccountCost: &cost,
		UpstreamEndpoints: []usagestats.EndpointStat{{Endpoint: upstream}}, EndpointPaths: []usagestats.EndpointStat{{Endpoint: upstream}}}
	data, err = json.Marshal(IntegrationUsageStatsFromStats(stats))
	require.NoError(t, err)
	require.Contains(t, string(data), `"total_points_cost":12.5`)
	for _, forbidden := range []string{upstream, "upstream", "account", "endpoint_paths"} {
		require.NotContains(t, string(data), forbidden)
	}
}
