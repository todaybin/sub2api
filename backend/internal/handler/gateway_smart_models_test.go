package handler

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSmartModelsUnionAliasesAndEmpty(t *testing.T) {
	repo := &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
		1: {{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"same": "gpt-5", "alias": "gpt-5"}}}},
		2: {{ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"same": "gpt-5"}}}},
	}}
	h := newGatewayModelsHandlerForTest(repo)
	key := &service.APIKey{RoutingMode: "smart", SmartGroups: []*service.Group{{ID: 1, Status: service.StatusActive, Platform: service.PlatformOpenAI}, {ID: 2, Status: service.StatusActive, Platform: service.PlatformOpenAI}}}
	for _, empty := range []bool{false, true} {
		if empty {
			repo.byGroup = map[int64][]service.Account{}
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Set(string(middleware.ContextKeyAPIKey), key)
		h.Models(c)
		require.Equal(t, http.StatusOK, w.Code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		data := got["data"].([]any)
		if empty {
			require.Empty(t, data)
		} else {
			require.Len(t, data, 2)
			require.Equal(t, "alias", data[0].(map[string]any)["id"])
			require.Equal(t, "same", data[1].(map[string]any)["id"])
		}
		require.Nil(t, key.GroupID)
	}
}
func TestSmartCodexDuplicateCapabilitiesConservative(t *testing.T) {
	a := map[string]any{"slug": "same", "supports_tools": true, "context_window": float64(200000), "supported_reasoning_levels": []any{"low", "high"}, "input_modalities": []any{"text", "image"}}
	b := map[string]any{"slug": "same", "supports_tools": false, "context_window": float64(100000), "supported_reasoning_levels": []any{"low"}, "input_modalities": []any{"text"}}
	got := conservativeSmartDescriptor(a, b)
	require.False(t, got["supports_tools"].(bool))
	require.Equal(t, float64(100000), got["context_window"])
	require.Equal(t, []any{"low"}, got["supported_reasoning_levels"])
	require.Equal(t, []any{"text"}, got["input_modalities"])
	require.True(t, a["supports_tools"].(bool))
	require.Equal(t, float64(200000), a["context_window"])
	missing := conservativeSmartDescriptor(a, map[string]any{"slug": "same"})
	require.NotContains(t, missing, "context_window")
	require.False(t, missing["supports_tools"].(bool))
}
