package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodeBuddyEndpointForRegion(t *testing.T) {
	endpoint, err := CodeBuddyEndpointForRegion(CodeBuddyRegionDomestic)
	require.NoError(t, err)
	require.Equal(t, CodeBuddyDomesticEndpoint, endpoint)

	endpoint, err = CodeBuddyEndpointForRegion(" INTERNATIONAL ")
	require.NoError(t, err)
	require.Equal(t, CodeBuddyInternationalEndpoint, endpoint)

	_, err = CodeBuddyEndpointForRegion("global")
	require.Error(t, err)
}

func TestCodeBuddyModelsForRegionUsesSeparateFallbacks(t *testing.T) {
	require.Contains(t, CodeBuddyModelsForRegion(CodeBuddyRegionDomestic), "glm-5.2")
	require.Contains(t, CodeBuddyModelsForRegion(CodeBuddyRegionInternational), "gpt-5.6-sol")
	require.NotContains(t, CodeBuddyModelsForRegion(CodeBuddyRegionInternational), "glm-5.2")
}

func TestCodeBuddyModelBillingMultiplierUsesCatalogAndSafeDefaults(t *testing.T) {
	value := 0.79
	account := &Account{
		Platform: PlatformCodeBuddy,
		Extra: map[string]any{
			"codebuddy_model_catalog": []any{
				map[string]any{"id": "glm-5.2", "credits_multiplier": value},
				map[string]any{"id": "broken", "credits_multiplier": 0.0},
			},
		},
	}
	require.InDelta(t, 0.79, CodeBuddyModelBillingMultiplier(account, "glm-5.2"), 1e-12)
	require.InDelta(t, 0.01, CodeBuddyModelBillingMultiplier(account, "broken"), 1e-12)
	require.InDelta(t, 1.0, CodeBuddyModelBillingMultiplier(account, "missing"), 1e-12)
}

func TestCodeBuddyProviderCostMultiplierUsesAbstractCreditCost(t *testing.T) {
	account := &Account{
		Platform: PlatformCodeBuddy,
		Extra: map[string]any{
			"codebuddy_model_catalog": []any{
				map[string]any{"id": "hy3", "credits_multiplier": 0.79},
			},
		},
	}
	usage := ClaudeUsage{InputTokens: 800, OutputTokens: 200}
	baseCost := 0.0005
	expectedCost := float64(CodeBuddyBillableTokens(usage)) * CodeBuddyUpstreamCostPerToken(0.79)
	require.InDelta(t, expectedCost/baseCost, CodeBuddyProviderCostMultiplier(account, baseCost, usage, "hy3"), 1e-12)
	require.InDelta(t, 0.79*70/(2000*31874), CodeBuddyUpstreamCostPerToken(0.79), 1e-15)
}

func TestCodeBuddyProviderCostMultiplierUsesConfiguredAbstractBaseline(t *testing.T) {
	account := &Account{
		Platform: PlatformCodeBuddy,
		Extra: map[string]any{
			"codebuddy_model_catalog": []any{
				map[string]any{"id": "hy3", "credits_multiplier": 2.0},
			},
		},
	}
	usage := ClaudeUsage{InputTokens: 1000}
	baseCost := 0.001
	configuredBase := 0.000002
	expected := float64(CodeBuddyBillableTokens(usage)) * 2 * configuredBase / baseCost
	require.InDelta(t, expected, CodeBuddyProviderCostMultiplierWithBase(account, baseCost, usage, configuredBase, "hy3"), 1e-12)
}

func TestCodeBuddyBillableTokensDoesNotDoubleCountCache(t *testing.T) {
	usage := ClaudeUsage{InputTokens: 100, OutputTokens: 50, CacheCreationInputTokens: 20, CacheReadInputTokens: 30}
	require.Equal(t, 200, CodeBuddyBillableTokens(usage))
}

func TestCodeBuddyRegionForAccountDefaultsAndUsesEndpoint(t *testing.T) {
	require.Equal(t, CodeBuddyRegionDomestic, CodeBuddyRegionForAccount(&Account{Platform: PlatformCodeBuddy}))
	require.Equal(t, CodeBuddyRegionInternational, CodeBuddyRegionForAccount(&Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"base_url": CodeBuddyInternationalEndpoint}}))
	require.Equal(t, CodeBuddyRegionDomestic, CodeBuddyRegionForAccount(&Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{"region": CodeBuddyRegionDomestic, "base_url": CodeBuddyInternationalEndpoint}}))
}

func TestCodeBuddyRefreshExpiry(t *testing.T) {
	require.Equal(t, "2026-01-02T03:04:05Z", codeBuddyRefreshExpiry(map[string]any{"expires_at": "2026-01-02T03:04:05Z"}))
	expiry := codeBuddyRefreshExpiry(map[string]any{"expires_in": float64(300)})
	expiryTime, err := time.Parse(time.RFC3339, expiry)
	require.NoError(t, err)
	require.InDelta(t, 300, time.Until(expiryTime).Seconds(), 3)
}

func TestCodeBuddyModelsFromObject(t *testing.T) {
	models := codeBuddyModelsFromObject(map[string]any{
		"available_models": []any{"auto", map[string]any{"id": "glm-5v-turbo"}, "auto"},
	})
	require.Equal(t, []string{"auto", "glm-5v-turbo"}, models)
	require.Nil(t, codeBuddyModelsFromObject(map[string]any{"models": "auto"}))

	models = codeBuddyModelsFromObject(map[string]any{
		"data": map[string]any{
			"models": []any{
				map[string]any{"id": "deepseek-v4-flash", "name": "DeepSeek-V4-Flash"},
				map[string]any{"id": "glm-5.2"},
			},
		},
	})
	require.Equal(t, []string{"deepseek-v4-flash", "glm-5.2"}, models)
}

func TestCodeBuddyModelCatalogRetainsCredits(t *testing.T) {
	catalog := codeBuddyModelCatalogFromObject(map[string]any{
		"data": map[string]any{"models": []any{
			map[string]any{"id": "gpt-5.6", "name": "GPT-5.6", "credits": "x0.79 credits", "supportsReasoning": true, "reasoning": map[string]any{"effort": "high"}},
		}},
	})
	require.Len(t, catalog, 1)
	require.Equal(t, "gpt-5.6", catalog[0].ID)
	require.Equal(t, "x0.79 credits", catalog[0].Credits)
	require.NotNil(t, catalog[0].CreditsMultiplier)
	require.InDelta(t, 0.79, *catalog[0].CreditsMultiplier, 0.0001)
	require.True(t, catalog[0].SupportsReasoning)
	require.Equal(t, "high", catalog[0].ReasoningEffort)
}

func TestCodeBuddyEnterpriseCatalogAcceptsTopLevelArray(t *testing.T) {
	catalog := codeBuddyModelCatalogFromValue([]any{
		map[string]any{"id": "custom:team-model", "name": "Team Model", "creditsMultiplier": 2.5},
		map[string]any{"id": "gpt-5.6-sol", "name": "GPT-5.6-Sol", "credits": "x3.47 credits"},
	})
	catalog = markCodeBuddyEnterprise(catalog)
	require.Len(t, catalog, 2)
	require.True(t, catalog[0].IsEnterprise)
	require.NotNil(t, catalog[0].CreditsMultiplier)
	require.InDelta(t, 2.5, *catalog[0].CreditsMultiplier, 0.0001)
	require.True(t, catalog[1].IsEnterprise)
}

func TestCodeBuddyCatalogAcceptsModelKeyedConfiguration(t *testing.T) {
	catalog := codeBuddyModelCatalogFromValue(map[string]any{
		"models": map[string]any{
			"auto":         map[string]any{"name": "Auto"},
			"gpt-5.6-luna": map[string]any{"name": "GPT-5.6-Luna", "creditsMultiplier": 0.14},
		},
	})
	require.Len(t, catalog, 2)
	ids := []string{catalog[0].ID, catalog[1].ID}
	require.ElementsMatch(t, []string{"auto", "gpt-5.6-luna"}, ids)
}

func TestCodeBuddyTestPayloadStartsWithSystemMessage(t *testing.T) {
	payload := createOpenAIChatCompletionsTestPayload("gpt-5.6-luna", "hi")
	prependCodeBuddySystemPrompt(payload)
	messages, ok := payload["messages"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	require.Equal(t, "system", messages[0]["role"])
	require.Equal(t, "user", messages[1]["role"])
}

func TestMergeCodeBuddyEnterpriseCatalog(t *testing.T) {
	base := []CodeBuddyModel{{ID: "glm-5.3", Credits: "x0.79 credits"}, {ID: "gpt-5.6-sol", Name: "old"}}
	enterprise := markCodeBuddyEnterprise([]CodeBuddyModel{{ID: "gpt-5.6-sol", Name: "GPT-5.6-Sol", Credits: "x3.47 credits"}, {ID: "gpt-5.6-terra", Credits: "x1.39 credits"}})
	merged := mergeCodeBuddyModelCatalog(base, enterprise)
	require.Equal(t, []string{"auto", "glm-5.3", "gpt-5.6-sol", "gpt-5.6-terra"}, codeBuddyModelIDs(merged))
	require.Equal(t, "x3.47 credits", merged[1].Credits)
	require.True(t, merged[1].IsEnterprise)
	require.True(t, merged[2].IsEnterprise)
}

func TestCodeBuddyEnterpriseIDFromNestedAccount(t *testing.T) {
	require.Equal(t, "ent-42", codeBuddyEnterpriseIDFromObject(map[string]any{
		"enterprises": []any{map[string]any{"id": "ent-42"}},
	}))
	require.Equal(t, "user-42", codeBuddyUserIDFromValue(map[string]any{
		"accounts": []any{map[string]any{"uid": "user-42", "enterpriseId": "ent-42"}},
	}, 0))
}

func TestCodeBuddyPluginAccountSelectionMatchesVSCode(t *testing.T) {
	value := map[string]any{"data": map[string]any{"accounts": []any{
		map[string]any{"pluginEnabled": false, "lastLogin": true, "uid": "wrong", "enterpriseId": "wrong-ent"},
		map[string]any{"pluginEnabled": true, "lastLogin": false, "uid": "first", "enterpriseId": "ent-first"},
		map[string]any{"pluginEnabled": true, "lastLogin": true, "uid": "active", "enterpriseId": 42.0},
	}}}
	selected, ok := codeBuddyPluginAccountFromValue(value).(map[string]any)
	require.True(t, ok)
	require.Equal(t, "active", codeBuddyUserIDFromValue(selected, 0))
	require.Equal(t, "42", codeBuddyEnterpriseIDFromObject(selected))
}

func TestCodeBuddyCatalogFetchUsesPluginUAAndEnterpriseEndpoint(t *testing.T) {
	var enterpriseHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/plugin/accounts":
			_, _ = w.Write([]byte(`{"data":{"accounts":[{"pluginEnabled":true,"lastLogin":true,"uid":"u-1","enterpriseId":"ent-1"}]}}`))
		case "/console/enterprises/ent-1/config/models":
			enterpriseHeaders = r.Header.Clone()
			_, _ = w.Write([]byte(`[{"id":"glm-5.2","name":"GLM-5.2","credits":"x0.79 credits"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := &CodeBuddyOAuthService{
		sessions: make(map[string]*codeBuddySession),
		clientFactory: func(string) (*http.Client, error) {
			return server.Client(), nil
		},
	}
	sess := &codeBuddySession{Endpoint: server.URL, Region: CodeBuddyRegionDomestic}
	result := svc.fetchModelCatalogDetailed(context.Background(), sess, "token", "", "")
	require.True(t, result.live)
	require.Equal(t, "u-1", result.uid)
	require.Equal(t, "ent-1", result.enterprise)
	require.Contains(t, result.models, "glm-5.2")
	require.Equal(t, codeBuddyUserAgent, enterpriseHeaders.Get("User-Agent"))
	require.Equal(t, codeBuddyVSCodeVersion, enterpriseHeaders.Get("X-IDE-Version"))
	require.Equal(t, codeBuddyPluginVersion, enterpriseHeaders.Get("X-Product-Version"))
	require.True(t, strings.HasPrefix(enterpriseHeaders.Get("User-Agent"), "VSCode/"))
}

func TestCodeBuddyModelIDsForRegionFiltersMixedCatalog(t *testing.T) {
	ids := CodeBuddyModelIDsForRegion([]string{"auto", "gpt-5.6-sol", "glm-5.2", "claude-sonnet-4-5"}, CodeBuddyRegionDomestic)
	require.Equal(t, []string{"auto", "glm-5.2"}, ids)
	ids = CodeBuddyModelIDsForRegion([]string{"auto", "gpt-5.6-sol", "glm-5.2"}, CodeBuddyRegionInternational)
	require.Equal(t, []string{"auto", "gpt-5.6-sol"}, ids)
}

func TestCodeBuddyCatalogFilterRemovesMixedRegionModels(t *testing.T) {
	catalog := filterCodeBuddyCatalogForRegion([]CodeBuddyModel{
		{ID: "auto"}, {ID: "glm-5.2"}, {ID: "gpt-5.6-luna"}, {ID: "custom:team-model"},
	}, CodeBuddyRegionDomestic)
	ids := make([]string, 0, len(catalog))
	for _, model := range catalog {
		ids = append(ids, model.ID)
	}
	require.Equal(t, []string{"auto", "glm-5.2", "custom:team-model"}, ids)
}

func TestCodeBuddyTokenRefresher(t *testing.T) {
	refresher := NewCodeBuddyTokenRefresher(&CodeBuddyOAuthService{})
	account := &Account{ID: 42, Platform: PlatformCodeBuddy, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "refresh",
		"expires_at":    time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}}
	require.True(t, refresher.CanRefresh(account))
	require.True(t, refresher.NeedsRefresh(account, 10*time.Minute))
	require.Equal(t, "codebuddy:42", refresher.CacheKey(account))
}
