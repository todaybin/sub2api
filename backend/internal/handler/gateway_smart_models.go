package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Discovery never chooses a billing group, and an empty union stays empty.
func (h *GatewayHandler) smartModels(c *gin.Context, key *service.APIKey, codex bool) {
	entries := map[string]map[string]any{}
	forced, _ := middleware.GetForcePlatformFromContext(c)
	for _, group := range key.SmartGroups {
		if group == nil || !group.IsActive() || (forced != "" && group.Platform != forced && group.Platform != service.PlatformComposite) {
			continue
		}
		var targets []string
		if forced != "" {
			targets = []string{forced}
		}
		ids, err := h.gatewayService.SmartModelCatalogForEndpoint(c.Request.Context(), group, service.CompositeRouteEndpointAny, targets...)
		if err != nil {
			writeOpenAIModelsError(c, http.StatusInternalServerError, "api_error", "Failed to load smart model catalogue")
			return
		}
		var body []byte
		if group.Platform == service.PlatformOpenAI && group.CodexModelsManifestConfig.Enabled && h.openAIGatewayService != nil {
			var response *service.OpenAIModelsResponse
			if codex {
				response, _, err = h.openAIGatewayService.FetchPinnedCodexModelsManifest(c.Request.Context(), group, c.Query("client_version"))
			} else {
				response, _, err = h.openAIGatewayService.FetchPinnedOpenAIModelsList(c.Request.Context(), group, h.maxAccountSwitches, "")
			}
			if err != nil && !(errors.Is(err, service.ErrNoPinnedCodexModelsAccounts) && group.CodexModelsManifestConfig.FallbackToScheduler) {
				writeOpenAIModelsError(c, http.StatusServiceUnavailable, "upstream_error", "Failed to load pinned smart model catalogue")
				return
			}
			if response != nil {
				body = response.Body
			}
		}
		if len(body) == 0 && codex {
			ids = service.FilterCodexModelIDsForGroup(ids, group)
			body, err = h.gatewayService.BuildCodexModelsManifestForGroup(c.Request.Context(), group, forced, ids)
			if err != nil {
				writeOpenAIModelsError(c, http.StatusInternalServerError, "api_error", "Failed to build smart model manifest")
				return
			}
		}
		if len(body) > 0 {
			field, idField := "data", "id"
			if codex {
				field, idField = "models", "slug"
			}
			var envelope map[string]json.RawMessage
			var models []map[string]any
			if json.Unmarshal(body, &envelope) != nil || json.Unmarshal(envelope[field], &models) != nil {
				writeOpenAIModelsError(c, http.StatusBadGateway, "upstream_error", "Invalid smart model catalogue")
				return
			}
			for _, model := range models {
				id, _ := model[idField].(string)
				if id == "" || (group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(id)) {
					continue
				}
				if codex && len(service.FilterCodexModelIDsForGroup([]string{id}, group)) == 0 {
					continue
				}
				if old := entries[id]; old != nil && codex {
					entries[id] = conservativeSmartDescriptor(old, model)
				} else if old == nil {
					entries[id] = model
				}
			}
		} else {
			for _, id := range ids {
				if entries[id] == nil {
					entries[id] = map[string]any{"id": id, "object": "model", "created": int64(0), "owned_by": "sub2api"}
				}
			}
		}
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, entries[id])
	}
	if !codex {
		writeModelsListResponse(c, models)
		return
	}
	body, err := json.Marshal(gin.H{"models": models})
	if err != nil {
		writeOpenAIModelsError(c, http.StatusInternalServerError, "api_error", "Failed to encode smart model manifest")
		return
	}
	etag := service.CodexModelsManifestETag(body)
	c.Header("ETag", etag)
	if service.CodexModelsManifestETagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json", body)
}

// Duplicate public IDs must only advertise capabilities shared by every route.
func conservativeSmartDescriptor(a, b map[string]any) map[string]any {
	result := make(map[string]any, len(a))
	for k, v := range a {
		result[k] = v
	}
	for k, v := range a {
		switch value := v.(type) {
		case bool:
			other, ok := b[k].(bool)
			result[k] = ok && value && other
		case float64:
			if strings.Contains(k, "context") || strings.Contains(k, "max_") {
				other, ok := b[k].(float64)
				if !ok {
					delete(result, k)
				} else if other < value {
					result[k] = other
				}
			}
		case []any:
			if strings.Contains(k, "supported") || strings.Contains(k, "modalit") {
				other, _ := b[k].([]any)
				common := []any{}
				for _, x := range value {
					left, _ := json.Marshal(x)
					for _, y := range other {
						right, _ := json.Marshal(y)
						if string(left) == string(right) || (k == "supported_reasoning_levels" && smartReasoningEffort(x) != "" && smartReasoningEffort(x) == smartReasoningEffort(y)) {
							common = append(common, x)
							break
						}
					}
				}
				result[k] = common
			}
		case map[string]any:
			other, _ := b[k].(map[string]any)
			result[k] = conservativeSmartDescriptor(value, other)
		}
	}
	for k, v := range b {
		if _, exists := a[k]; !exists {
			if _, ok := v.(bool); ok {
				result[k] = false
			}
		}
	}
	if levels, ok := result["supported_reasoning_levels"].([]any); ok {
		defaultEffort, _ := result["default_reasoning_level"].(string)
		valid := false
		for _, level := range levels {
			valid = valid || smartReasoningEffort(level) == defaultEffort
		}
		if !valid {
			delete(result, "default_reasoning_level")
			if len(levels) > 0 {
				result["default_reasoning_level"] = smartReasoningEffort(levels[0])
			}
		}
	}
	return result
}

func smartReasoningEffort(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		text, _ := object["effort"].(string)
		return text
	}
	return ""
}

func (h *GatewayHandler) smartGeminiModels(c *gin.Context, key *service.APIKey, retrieve bool) {
	seen := map[string]bool{}
	forced, _ := middleware.GetForcePlatformFromContext(c)
	for _, group := range key.SmartGroups {
		if group == nil || (group.Platform != service.PlatformGemini && group.Platform != service.PlatformAntigravity && group.Platform != service.PlatformComposite) || (forced != "" && group.Platform != forced && group.Platform != service.PlatformComposite) {
			continue
		}
		ids, err := h.gatewayService.SmartModelCatalogForEndpoint(c.Request.Context(), group, service.CompositeRouteEndpointGemini, service.PlatformGemini, service.PlatformAntigravity)
		if err != nil {
			googleError(c, http.StatusInternalServerError, "Failed to load smart model catalogue")
			return
		}
		for _, id := range ids {
			if group.Platform != service.PlatformAntigravity || strings.HasPrefix(id, "gemini-") {
				seen[id] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if retrieve {
		id := strings.TrimPrefix(c.Param("model"), "models/")
		if !seen[id] {
			googleError(c, http.StatusNotFound, "Model is not available for this key")
			return
		}
		c.JSON(http.StatusOK, gemini.FallbackModel(id))
		return
	}
	models := make([]gemini.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, gemini.FallbackModel(id))
	}
	c.JSON(http.StatusOK, gemini.ModelsListResponse{Models: models})
}
