package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIntegrationCapabilitiesIntersectAndUnknown(t *testing.T) {
	a := ModelParameterCapabilities{Supported: capabilityBool(true), ContextWindow: 128000, MaxOutputTokens: 8192, InputModalities: []string{"text", "image"}, Tools: capabilityBool(true), Reasoning: &ModelReasoningCapabilities{Supported: capabilityBool(true), Mode: "effort", Efforts: []string{"high", "ultra", "persistent"}, DefaultEffort: "high"}}
	b := a
	b.ContextWindow = 64000
	b.MaxOutputTokens = 4096
	b.InputModalities = []string{"text"}
	b.Reasoning = &ModelReasoningCapabilities{Supported: capabilityBool(true), Mode: "effort", Efforts: []string{"high", "persistent"}, DefaultEffort: "high"}
	got := IntersectModelParameters([]ModelParameterCapabilities{a, b})
	require.Equal(t, int64(64000), got.ContextWindow)
	require.Equal(t, int64(4096), got.MaxOutputTokens)
	require.Equal(t, []string{"high", "persistent"}, got.Reasoning.Efforts)
	require.Equal(t, []string{"text"}, got.InputModalities)
	unknown := IntersectModelParameters([]ModelParameterCapabilities{a, {}})
	require.Zero(t, unknown.ContextWindow)
	require.Nil(t, unknown.Tools)
	require.Empty(t, unknown.InputModalities)
	require.Nil(t, unknown.Reasoning)
	got = IntersectModelParameters([]ModelParameterCapabilities{a, {Tools: capabilityBool(false)}})
	require.Equal(t, false, *got.Tools)
}
func TestIntegrationCapabilitiesFollowAliasAndPreserveRawEfforts(t *testing.T) {
	a := Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": "upstream"}}}
	a.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Source: "fixture", SyncedAt: "2026-11-15T00:00:00Z", Models: map[string]UpstreamModelMetadata{"upstream": {ID: "upstream", Reasoning: capabilityBool(true), SupportedReasoningLevels: []string{"high", "ultra", "persistent", "Custom.Level"}, ContextWindow: 128000, MaxOutputTokens: 8192}}})
	svc := &GatewayService{accountRepo: &smartCatalogRepo{accounts: []Account{a}}}
	got, err := svc.SmartModelCapabilities(context.Background(), &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}, []string{"public"})
	require.NoError(t, err)
	require.Equal(t, []string{"high", "ultra", "persistent", "Custom.Level"}, got["public"].Endpoints["responses"].Reasoning.Efforts)
	require.Equal(t, int64(8192), got["public"].MaxOutputTokens)
	require.Equal(t, "2026-11-15T00:00:00Z", got["public"].ObservedAt)
	_, metadata, err := extractUpstreamModelCatalog([]byte(`{"data":[{"id":"raw","reasoning":true,"supported_reasoning_levels":["persistent","Custom.Level","ultra"]}] }`), false)
	require.NoError(t, err)
	require.Equal(t, []string{"persistent", "Custom.Level", "ultra"}, metadata["raw"].SupportedReasoningLevels)
}
func TestIntegrationCapabilitiesMergeByEndpoint(t *testing.T) {
	a := ModelCapabilities{Version: 1, Source: "upstream-snapshot", Endpoints: map[string]ModelParameterCapabilities{"responses": {Supported: capabilityBool(true), ContextWindow: 1000, MaxOutputTokens: 100}}}
	b := ModelCapabilities{Version: 1, Source: "upstream-snapshot", Endpoints: map[string]ModelParameterCapabilities{"messages": {Supported: capabilityBool(true), ContextWindow: 2000, MaxOutputTokens: 200}}}
	got := MergeModelCapabilities([]ModelCapabilities{a, b})
	require.Equal(t, int64(1000), got.Endpoints["responses"].ContextWindow)
	require.Equal(t, int64(2000), got.Endpoints["messages"].ContextWindow)
	require.Equal(t, int64(1000), got.ContextWindow)
	got = MergeModelCapabilities([]ModelCapabilities{a, {}})
	require.Zero(t, got.Endpoints["responses"].ContextWindow)
}
