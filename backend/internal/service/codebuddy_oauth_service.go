package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

const (
	CodeBuddyRegionDomestic      = "domestic"
	CodeBuddyRegionInternational = "international"
	CodeBuddyDomesticAuthDomain  = "www.codebuddy.cn"
	CodeBuddyGlobalAuthDomain    = "www.codebuddy.ai"
	codeBuddySessionTTL          = 5 * time.Minute
)

// CodeBuddyModels is the domestic CodeBuddy fallback catalog. The real catalog
// is fetched from the enterprise model configuration after OAuth; this list is
// only used when the upstream is temporarily unavailable or the account has no
// enterprise scope.
var CodeBuddyModels = []string{
	// Keep this list aligned with the IDs used by the bundled CodeBuddy
	// converter. A live OAuth catalog always takes precedence over this
	// compatibility fallback.
	"glm-5.2", "glm-5.1", "glm-5v-turbo", "kimi-k2.7", "kimi-k2.6", "kimi-k2.5",
	"deepseek-v4-pro", "deepseek-v4-flash", "minimax-m3-pay", "hy3-preview-agent", "auto",
}

// CodeBuddyInternationalModels is only a last-resort catalog. Successful
// OAuth synchronization always replaces it with the account's /v3/config
// response, which is the authoritative list for that international tenant.
var CodeBuddyInternationalModels = []string{
	"auto", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
	"gpt-5.3-codex", "gemini-3.5-flash", "claude-sonnet-4-5", "claude-opus-4-5", "gpt-5",
}

var codeBuddyDomesticOnlyModels = map[string]struct{}{
	"hy3": {}, "hy3-preview-agent": {}, "glm-5.3": {}, "glm-5.2": {}, "glm-5.1": {}, "glm-5v-turbo": {},
	"kimi-k3": {}, "kimi-k2.7": {}, "kimi-k2.7-code": {}, "kimi-k2.6": {}, "kimi-k2.5": {},
	"minimax-m3": {}, "minimax-m3-pay": {}, "deepseek-v4-pro": {}, "deepseek-v4-flash": {},
}

var codeBuddyInternationalOnlyModels = map[string]struct{}{
	"gpt-5.6-sol": {}, "gpt-5.6-terra": {}, "gpt-5.6-luna": {}, "gpt-5.5": {}, "gpt-5.4": {},
	"gpt-5.3-codex": {}, "gemini-3.5-flash": {}, "claude-sonnet-4-5": {}, "claude-opus-4-5": {}, "gpt-5": {},
}

// CodeBuddyModel contains model metadata returned by CodeBuddy's product
// configuration endpoint. IDs remain the values used by request routing.
type CodeBuddyModel struct {
	ID                string   `json:"id"`
	Name              string   `json:"name,omitempty"`
	Credits           string   `json:"credits,omitempty"`
	CreditsMultiplier *float64 `json:"credits_multiplier,omitempty"`
	MaxInputTokens    int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int64    `json:"max_output_tokens,omitempty"`
	SupportsToolCall  bool     `json:"supports_tool_call,omitempty"`
	SupportsImages    bool     `json:"supports_images,omitempty"`
	SupportsReasoning bool     `json:"supports_reasoning,omitempty"`
	ReasoningEffort   string   `json:"reasoning_effort,omitempty"`
	Vendor            string   `json:"vendor,omitempty"`
	IsEnterprise      bool     `json:"is_enterprise,omitempty"`
	Region            string   `json:"region,omitempty"`
}

// CodeBuddyRegionForAccount returns the persisted region for an account. Older
// accounts may not have a region credential, so the endpoint is used as the
// compatibility fallback. The result is always one of the two supported
// regions and defaults to domestic for safety.
func CodeBuddyRegionForAccount(account *Account) string {
	if account == nil {
		return CodeBuddyRegionDomestic
	}
	for _, value := range []string{account.GetCredential("region"), account.GetExtraString("codebuddy_region")} {
		if strings.EqualFold(strings.TrimSpace(value), CodeBuddyRegionInternational) {
			return CodeBuddyRegionInternational
		}
		if strings.EqualFold(strings.TrimSpace(value), CodeBuddyRegionDomestic) {
			return CodeBuddyRegionDomestic
		}
	}
	endpoint := strings.ToLower(strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/"))
	if endpoint == strings.ToLower(CodeBuddyInternationalEndpoint) || strings.Contains(endpoint, "codebuddy.ai") {
		return CodeBuddyRegionInternational
	}
	return CodeBuddyRegionDomestic
}

func CodeBuddyModelsForRegion(region string) []string {
	if strings.EqualFold(strings.TrimSpace(region), CodeBuddyRegionInternational) {
		return append([]string(nil), CodeBuddyInternationalModels...)
	}
	return append([]string(nil), CodeBuddyModels...)
}

type CodeBuddyOAuthStartInput struct {
	AccountID          int64
	Region             string
	Name               string
	Notes              string
	ProxyID            *int64
	GroupIDs           []int64
	Concurrency        int
	LoadFactor         *int
	Priority           int
	RateMultiplier     *float64
	ExpiresAt          *int64
	AutoPauseOnExpired *bool
	ModelMapping       map[string]string
	ReferenceCostUnits float64
	ReferenceCredits   float64
	TokensPerCredit    float64
}

type CodeBuddyOAuthStartResult struct {
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
	Region    string `json:"region"`
	ExpiresAt int64  `json:"expires_at"`
}

type CodeBuddyOAuthPollResult struct {
	Status    string   `json:"status"`
	SessionID string   `json:"session_id"`
	Account   *Account `json:"account,omitempty"`
	Message   string   `json:"message,omitempty"`
}

type codeBuddySession struct {
	ID                 string
	AccountID          int64
	Region             string
	Endpoint           string
	Domain             string
	State              string
	Name               string
	Notes              string
	ProxyID            *int64
	GroupIDs           []int64
	Concurrency        int
	LoadFactor         *int
	Priority           int
	RateMultiplier     *float64
	AccountExpiresAt   *int64
	AutoPauseOnExpired *bool
	ModelMapping       map[string]string
	ReferenceCostUnits float64
	ReferenceCredits   float64
	TokensPerCredit    float64
	SessionExpiresAt   time.Time
	AccessToken        string
	RefreshToken       string
}

type CodeBuddyOAuthService struct {
	admin         AdminService
	mu            sync.Mutex
	sessions      map[string]*codeBuddySession
	clientFactory func(proxyURL string) (*http.Client, error)
}

func NewCodeBuddyOAuthService(admin AdminService) *CodeBuddyOAuthService {
	return &CodeBuddyOAuthService{admin: admin, sessions: make(map[string]*codeBuddySession), clientFactory: func(proxyURL string) (*http.Client, error) {
		return httpclient.GetClient(httpclient.Options{ProxyURL: proxyURL, Timeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second})
	}}
}

func CodeBuddyEndpointForRegion(region string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case CodeBuddyRegionDomestic:
		return CodeBuddyDomesticEndpoint, nil
	case CodeBuddyRegionInternational:
		return CodeBuddyInternationalEndpoint, nil
	default:
		return "", fmt.Errorf("codebuddy region must be domestic or international")
	}
}

func CodeBuddyAuthDomainForRegion(region string) string {
	if strings.EqualFold(strings.TrimSpace(region), CodeBuddyRegionInternational) {
		return CodeBuddyGlobalAuthDomain
	}
	return CodeBuddyDomesticAuthDomain
}

func randomCodeBuddyID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *CodeBuddyOAuthService) proxyURL(ctx context.Context, id *int64) (string, error) {
	if id == nil || *id == 0 || s.admin == nil {
		return "", nil
	}
	p, err := s.admin.GetProxy(ctx, *id)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", errors.New("proxy not found")
	}
	return p.URL(), nil
}

func (s *CodeBuddyOAuthService) do(ctx context.Context, sess *codeBuddySession, method, path string, body []byte, headers map[string]string) ([]byte, int, error) {
	proxyURL, err := s.proxyURL(ctx, sess.ProxyID)
	if err != nil {
		return nil, 0, err
	}
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(sess.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

func decodeObject(raw []byte) (map[string]any, error) {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if data, ok := v["data"].(map[string]any); ok {
		for k, x := range data {
			if _, exists := v[k]; !exists {
				v[k] = x
			}
		}
	}
	return v, nil
}

func decodeJSONValue(raw []byte) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// codeBuddyUpstreamErrorDetail extracts only public error fields from an
// upstream JSON response. Do not include the full response: some upstream
// gateways echo request metadata that may contain account information.
func codeBuddyUpstreamErrorDetail(raw []byte) string {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	parts := make([]string, 0, 2)
	for _, key := range []string{"code", "message", "msg", "detail"} {
		if text, ok := codeBuddyStringValue(value[key]); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, key+"="+strings.TrimSpace(text))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func stringField(v map[string]any, keys ...string) string {
	for _, k := range keys {
		if x, ok := codeBuddyStringValue(v[k]); ok && strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func codeBuddyStringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), true
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	default:
		return "", false
	}
}

func codeBuddyEnterpriseIDFromObject(v map[string]any) string {
	return codeBuddyEnterpriseIDFromValue(v, 0)
}

func codeBuddyEnterpriseIDFromValue(value any, depth int) string {
	if depth > 5 {
		return ""
	}
	v, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if id := stringField(v, "enterpriseId", "enterprise_id", "tenantId", "tenant_id"); id != "" {
		return id
	}
	for _, key := range []string{"enterprise", "currentEnterprise", "current_enterprise", "activeEnterprise", "active_enterprise", "tenant"} {
		if nested, ok := v[key].(map[string]any); ok {
			if id := stringField(nested, "id", "enterpriseId", "enterprise_id", "tenantId", "tenant_id"); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"enterprises", "enterpriseList", "enterprise_list", "tenants"} {
		if values, ok := v[key].([]any); ok {
			for _, value := range values {
				if nested, ok := value.(map[string]any); ok {
					if id := stringField(nested, "id", "enterpriseId", "enterprise_id", "tenantId", "tenant_id"); id != "" {
						return id
					}
				}
			}
		}
	}
	for _, nested := range v {
		switch typed := nested.(type) {
		case map[string]any:
			if id := codeBuddyEnterpriseIDFromValue(typed, depth+1); id != "" {
				return id
			}
		case []any:
			for _, item := range typed {
				if id := codeBuddyEnterpriseIDFromValue(item, depth+1); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func codeBuddyUserIDFromValue(value any, depth int) string {
	if depth > 6 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		if id := stringField(typed, "uid", "userId", "user_id"); id != "" {
			return id
		}
		for _, nested := range typed {
			if id := codeBuddyUserIDFromValue(nested, depth+1); id != "" {
				return id
			}
		}
	case []any:
		for _, nested := range typed {
			if id := codeBuddyUserIDFromValue(nested, depth+1); id != "" {
				return id
			}
		}
	}
	return ""
}

func codeBuddyModelsFromObject(v map[string]any) []string {
	models := uniqueStrings(codeBuddyModelsFromValue(v))
	if len(models) == 0 {
		return nil
	}
	return models
}

func codeBuddyModelFromObject(raw map[string]any) (CodeBuddyModel, bool) {
	id := stringField(raw, "id", "modelId", "model_id", "model")
	if id == "" {
		return CodeBuddyModel{}, false
	}
	model := CodeBuddyModel{ID: id, Name: stringField(raw, "name", "displayName", "display_name"), Credits: stringField(raw, "credits", "credit")}
	for _, key := range []string{"creditsMultiplier", "credits_multiplier", "creditMultiplier", "credit_multiplier", "multiplier"} {
		if value, ok := raw[key]; ok {
			var parsed float64
			switch number := value.(type) {
			case float64:
				parsed = number
			case json.Number:
				parsed, _ = number.Float64()
			case string:
				parsed, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(number, "x")), 64)
			}
			if parsed > 0 {
				model.CreditsMultiplier = &parsed
				break
			}
		}
	}
	if model.Credits != "" {
		if i := strings.Index(strings.ToLower(model.Credits), "x"); i >= 0 {
			value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(model.Credits[i+1:]), "credits"))
			if multiplier, err := strconv.ParseFloat(value, 64); err == nil {
				model.CreditsMultiplier = &multiplier
			}
		}
	}
	for _, field := range []struct {
		keys []string
		dest *int64
	}{
		{[]string{"maxInputTokens", "max_input_tokens", "inputTokenLimit"}, &model.MaxInputTokens},
		{[]string{"maxOutputTokens", "max_output_tokens", "outputTokenLimit"}, &model.MaxOutputTokens},
	} {
		for _, key := range field.keys {
			if value, ok := raw[key]; ok {
				switch n := value.(type) {
				case float64:
					*field.dest = int64(n)
				case json.Number:
					if parsed, err := n.Int64(); err == nil {
						*field.dest = parsed
					}
				case string:
					if parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
						*field.dest = parsed
					}
				}
				if *field.dest != 0 {
					break
				}
			}
		}
	}
	model.SupportsToolCall = codeBuddyBoolField(raw, "supportsToolCall", "supports_tool_call", "toolCall")
	model.SupportsImages = codeBuddyBoolField(raw, "supportsImages", "supports_images", "multimodal")
	model.SupportsReasoning = codeBuddyBoolField(raw, "supportsReasoning", "supports_reasoning", "reasoning")
	model.ReasoningEffort = stringField(raw, "reasoningEffort", "reasoning_effort", "effort")
	if model.ReasoningEffort == "" {
		if reasoning, ok := raw["reasoning"].(map[string]any); ok {
			model.ReasoningEffort = stringField(reasoning, "effort")
		}
	}
	model.Vendor = stringField(raw, "vendor", "provider", "ownedBy", "owned_by")
	model.Region = stringField(raw, "region", "modelRegion", "model_region", "locale", "market")
	model.IsEnterprise = codeBuddyBoolField(raw, "isEnterprise", "is_enterprise", "enterprise", "enterpriseModel", "enterprise_model")
	if !model.IsEnterprise {
		for _, key := range []string{"scope", "source", "channel", "type", "modelType", "model_type"} {
			if value, ok := raw[key].(string); ok && strings.Contains(strings.ToLower(value), "enterprise") {
				model.IsEnterprise = true
				break
			}
		}
	}
	return model, true
}

func codeBuddyBoolField(v map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := v[key].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return false
}

func codeBuddyModelCatalogFromValue(value any) []CodeBuddyModel {
	switch raw := value.(type) {
	case []any:
		var result []CodeBuddyModel
		for _, item := range raw {
			result = append(result, codeBuddyModelCatalogFromValue(item)...)
		}
		return result
	case map[string]any:
		result := make([]CodeBuddyModel, 0, 1)
		if model, ok := codeBuddyModelFromObject(raw); ok {
			result = append(result, model)
		}
		knownKeys := map[string]struct{}{}
		for _, key := range []string{"data", "models", "availableModels", "available_models", "modelList", "model_list", "modelConfig", "model_config", "llmModels", "llm_models", "config"} {
			knownKeys[key] = struct{}{}
			if nested, ok := raw[key]; ok {
				if _, isString := nested.(string); !isString {
					result = append(result, codeBuddyModelCatalogFromValue(nested)...)
				}
			}
		}
		// Some product configurations use an object keyed by model ID instead
		// of an array: {"gpt-5.6-luna": {"name": ...}}. Parse those entries
		// without treating unrelated configuration IDs as models.
		for key, nested := range raw {
			if _, known := knownKeys[key]; known {
				continue
			}
			if !looksLikeCodeBuddyModelID(key) {
				continue
			}
			child, ok := nested.(map[string]any)
			if !ok {
				continue
			}
			if stringField(child, "id", "modelId", "model_id", "model") == "" {
				copyChild := make(map[string]any, len(child)+1)
				for childKey, childValue := range child {
					copyChild[childKey] = childValue
				}
				copyChild["id"] = key
				child = copyChild
			}
			result = append(result, codeBuddyModelCatalogFromValue(child)...)
		}
		return result
	default:
		return nil
	}
}

func looksLikeCodeBuddyModelID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "auto" || strings.HasPrefix(value, "custom:") ||
		strings.HasPrefix(value, "gpt-") || strings.HasPrefix(value, "gemini-") ||
		strings.HasPrefix(value, "glm-") || strings.HasPrefix(value, "kimi-") ||
		strings.HasPrefix(value, "deepseek-") || strings.HasPrefix(value, "minimax-") ||
		strings.HasPrefix(value, "hy3") || strings.HasPrefix(value, "claude-")
}

func codeBuddyModelCatalogFromObject(v map[string]any) []CodeBuddyModel {
	models := codeBuddyModelCatalogFromValue(v)
	seen := make(map[string]struct{}, len(models))
	result := make([]CodeBuddyModel, 0, len(models))
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		result = append(result, model)
	}
	return result
}

func codeBuddyCatalogForIDs(ids []string) []CodeBuddyModel {
	fallback := map[string]struct {
		name       string
		multiplier float64
		enterprise bool
	}{
		"auto": {"Auto", 0.79, false}, "gpt-5.6-sol": {"GPT-5.6-Sol", 3.47, true},
		"gpt-5.6-terra": {"GPT-5.6-Terra", 1.39, true}, "gpt-5.6-luna": {"GPT-5.6-Luna", 0.14, true},
		"gpt-5.5": {"GPT-5.5", 3.31, true}, "gpt-5.4": {"GPT-5.4", 1.65, true},
		"gpt-5.3-codex": {"GPT-5.3-Codex", 1.25, true}, "gemini-3.5-flash": {"Gemini-3.5-Flash", 0.99, true},
		"hy3": {"Hy3", 0, false}, "hy3-preview-agent": {"Hy3 Preview Agent", 0, false},
		"glm-5.3": {"GLM-5.3", 0.79, false}, "glm-5.2": {"GLM-5.2", 0.79, false},
		"glm-5.1": {"GLM-5.1", 0.79, false}, "glm-5v-turbo": {"GLM-5v-Turbo", 0.71, false},
		"kimi-k3": {"Kimi-K3", 1.62, false}, "kimi-k2.7": {"Kimi-K2.7", 0.57, false},
		"kimi-k2.7-code": {"Kimi-K2.7-Code", 0.57, false}, "kimi-k2.6": {"Kimi-K2.6", 0.52, false},
		"kimi-k2.5": {"Kimi-K2.5", 0.52, false}, "minimax-m3": {"MiniMax-M3", 0.25, false},
		"minimax-m3-pay":  {"MiniMax-M3 Pay", 0.25, false},
		"deepseek-v4-pro": {"DeepSeek-V4-Pro", 0, false}, "deepseek-v4-flash": {"DeepSeek-V4-Flash", 0, false},
	}
	result := make([]CodeBuddyModel, 0, len(ids))
	for _, id := range uniqueStrings(ids) {
		model := CodeBuddyModel{ID: id, Name: id}
		if entry, ok := fallback[id]; ok {
			model.Name, model.CreditsMultiplier, model.IsEnterprise = entry.name, &entry.multiplier, entry.enterprise
			if id != "auto" {
				model.ReasoningEffort = "high"
			}
		}
		result = append(result, model)
	}
	return result
}

func markCodeBuddyEnterprise(catalog []CodeBuddyModel) []CodeBuddyModel {
	for i := range catalog {
		if catalog[i].IsEnterprise {
			continue
		}
		id := strings.ToLower(catalog[i].ID)
		// The public /v3/config response mixes built-in models with the
		// enterprise-only GPT/Gemini entries shown by the VSCode client.
		catalog[i].IsEnterprise = strings.HasPrefix(id, "gpt-5.6") ||
			strings.HasPrefix(id, "gpt-5.5") ||
			strings.HasPrefix(id, "gpt-5.4") ||
			strings.HasPrefix(id, "gpt-5.3-codex") ||
			strings.HasPrefix(id, "gemini-3.5") ||
			strings.HasPrefix(id, "custom:")
	}
	return catalog
}

// CodeBuddyCatalogForIDs provides metadata-shaped fallback entries for API
// responses when no live catalog is available.
func CodeBuddyCatalogForIDs(ids []string) []CodeBuddyModel { return codeBuddyCatalogForIDs(ids) }

// codeBuddyModelsFromValue accepts both the enterprise response shape
// {"data": [{"id": "..."}]} and the plugin response shapes used by older
// CodeBuddy deployments. It deliberately extracts IDs only from model-like
// objects so display names cannot accidentally become callable model IDs.
func codeBuddyModelsFromValue(value any) []string {
	switch raw := value.(type) {
	case []any:
		models := make([]string, 0, len(raw))
		for _, item := range raw {
			models = append(models, codeBuddyModelsFromValue(item)...)
		}
		return models
	case map[string]any:
		models := make([]string, 0)
		if model := stringField(raw, "id", "modelId", "model_id", "model"); model != "" {
			models = append(models, model)
		}
		for _, key := range []string{"data", "models", "availableModels", "available_models", "modelList", "model_list", "modelConfig", "model_config", "llmModels", "llm_models", "config"} {
			if nested, ok := raw[key]; ok {
				if _, isString := nested.(string); isString {
					continue
				}
				models = append(models, codeBuddyModelsFromValue(nested)...)
			}
		}
		return models
	case string:
		if model := strings.TrimSpace(raw); model != "" {
			return []string{model}
		}
		return nil
	default:
		return nil
	}
}

func codeBuddyEnterpriseModelsPath(enterpriseID string) string {
	return "/console/enterprises/" + url.PathEscape(strings.TrimSpace(enterpriseID)) + "/config/models"
}

func mergeCodeBuddyModelCatalog(base, overlay []CodeBuddyModel) []CodeBuddyModel {
	result := append([]CodeBuddyModel(nil), base...)
	positions := make(map[string]int, len(result))
	for i, model := range result {
		positions[model.ID] = i
	}
	for _, model := range overlay {
		if i, exists := positions[model.ID]; exists {
			result[i] = model
			continue
		}
		positions[model.ID] = len(result)
		result = append(result, model)
	}
	return result
}

func codeBuddyModelIDs(catalog []CodeBuddyModel) []string {
	ids := make([]string, 0, len(catalog)+1)
	ids = append(ids, "auto")
	for _, model := range catalog {
		ids = append(ids, model.ID)
	}
	return uniqueStrings(ids)
}

// filterCodeBuddyCatalogForRegion honors an explicit region marker when the
// upstream includes one. CodeBuddy's live catalog is otherwise authoritative:
// model IDs such as GPT/Gemini can legitimately be available in both regional
// products, so a hard-coded ID allowlist would hide valid upstream models.
func filterCodeBuddyCatalogForRegion(catalog []CodeBuddyModel, region string) []CodeBuddyModel {
	result := make([]CodeBuddyModel, 0, len(catalog))
	for _, model := range catalog {
		modelRegion := strings.ToLower(strings.TrimSpace(model.Region))
		if modelRegion != "" && modelRegion != strings.ToLower(strings.TrimSpace(region)) {
			continue
		}
		// Some upstream configuration responses are shared by both products and
		// do not include a region field. Apply the known product allowlists as a
		// second guard so domestic accounts never receive international GPT/
		// Gemini entries and international accounts never receive domestic-only
		// entries.
		if len(CodeBuddyModelIDsForRegion([]string{model.ID}, region)) == 0 {
			continue
		}
		result = append(result, model)
	}
	return result
}

// CodeBuddyModelIDsForRegion filters persisted/requested IDs using the same
// region catalog rules as live synchronization.
func CodeBuddyModelIDsForRegion(ids []string, region string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range uniqueStrings(ids) {
		normalizedID := strings.ToLower(strings.TrimSpace(id))
		if strings.EqualFold(strings.TrimSpace(region), CodeBuddyRegionInternational) {
			if _, domesticOnly := codeBuddyDomesticOnlyModels[normalizedID]; domesticOnly {
				continue
			}
		} else if _, internationalOnly := codeBuddyInternationalOnlyModels[normalizedID]; internationalOnly {
			continue
		}
		result = append(result, id)
	}
	return result
}

type codeBuddyCatalogFetchResult struct {
	models      []string
	catalog     []CodeBuddyModel
	live        bool
	diagnostics []string
	uid         string
	enterprise  string
}

func (s *CodeBuddyOAuthService) fetchModelCatalog(ctx context.Context, sess *codeBuddySession, token, uid, enterpriseID string) ([]string, []CodeBuddyModel, bool) {
	result := s.fetchModelCatalogDetailed(ctx, sess, token, uid, enterpriseID)
	return result.models, result.catalog, result.live
}

func (s *CodeBuddyOAuthService) fetchModelCatalogDetailed(ctx context.Context, sess *codeBuddySession, token, uid, enterpriseID string) codeBuddyCatalogFetchResult {
	result := codeBuddyCatalogFetchResult{}
	record := func(path string, status int, err error, parsed int) {
		if err != nil {
			result.diagnostics = append(result.diagnostics, path+": "+err.Error())
			return
		}
		if status >= 400 {
			result.diagnostics = append(result.diagnostics, fmt.Sprintf("%s: HTTP %d", path, status))
			return
		}
		if parsed == 0 {
			result.diagnostics = append(result.diagnostics, fmt.Sprintf("%s: HTTP %d, no model objects", path, status))
		}
	}
	recordResponse := func(path string, raw []byte, status int, err error, parsed int) {
		if status >= 400 && err == nil {
			if detail := codeBuddyUpstreamErrorDetail(raw); detail != "" {
				record(path, status, errors.New(detail), parsed)
				return
			}
		}
		record(path, status, err, parsed)
	}
	effectiveUserID := strings.TrimSpace(uid)
	effectiveEnterpriseID := strings.TrimSpace(enterpriseID)
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
		"User-Agent":    codeBuddyUserAgent,
		"X-Domain":      codeBuddySessionDomain(sess),
		"X-Product":     "SaaS",
		"X-IDE-Name":    "VSCode",
		// These headers are injected by the VS Code extension's product
		// configuration interceptor. Some tenants reject /v3/config with HTTP
		// 400 when the IDE/product version is absent.
		"X-IDE-Type":        "VSCode",
		"X-IDE-Version":     codeBuddyVSCodeVersion,
		"X-Product-Version": codeBuddyPluginVersion,
		"X-Requested-With":  "XMLHttpRequest",
	}
	if effectiveUserID != "" {
		headers["X-User-Id"] = effectiveUserID
	}
	if effectiveEnterpriseID != "" {
		headers["X-Enterprise-Id"] = effectiveEnterpriseID
		headers["X-Tenant-Id"] = effectiveEnterpriseID
	}
	// Existing accounts created before enterprise metadata was persisted may
	// have a valid token but an empty enterprise_id. The VS Code client obtains
	// the account list first, so do the same before requesting enterprise-only
	// models.
	if effectiveEnterpriseID == "" {
		raw, status, err := s.do(ctx, sess, http.MethodGet, "/v2/plugin/accounts", nil, headers)
		if err == nil && status < 400 {
			if value, decodeErr := decodeJSONValue(raw); decodeErr == nil {
				// The extension reads data.accounts, filters pluginEnabled
				// accounts, and prefers lastLogin. Match that selection so an
				// unrelated account cannot supply the wrong enterprise ID.
				selected := codeBuddyPluginAccountFromValue(value)
				if effectiveUserID == "" {
					effectiveUserID = codeBuddyUserIDFromValue(selected, 0)
					if effectiveUserID == "" {
						effectiveUserID = codeBuddyUserIDFromValue(value, 0)
					}
					if effectiveUserID != "" {
						headers["X-User-Id"] = effectiveUserID
					}
				}
				effectiveEnterpriseID = codeBuddyEnterpriseIDFromValue(selected, 0)
				if effectiveEnterpriseID == "" {
					effectiveEnterpriseID = codeBuddyEnterpriseIDFromValue(value, 0)
				}
				if effectiveEnterpriseID != "" {
					headers["X-Enterprise-Id"] = effectiveEnterpriseID
					headers["X-Tenant-Id"] = effectiveEnterpriseID
				}
			} else {
				recordResponse("/v2/plugin/accounts", raw, status, decodeErr, 0)
			}
		} else {
			recordResponse("/v2/plugin/accounts", raw, status, err, 0)
		}
	}
	// These are the remote product configuration paths used by different
	// CodeBuddy/VS Code extension versions. Newer builds default to /v3/config,
	// while older domestic deployments expose the same payload at /v2/config or
	// /config/models. Try them in order and accept only a response containing
	// model objects, so a successful but unrelated config response is ignored.
	var discovered []CodeBuddyModel
	for _, configPath := range []string{"/v3/config", "/v2/config", "/config/models"} {
		raw, status, err := s.do(ctx, sess, http.MethodGet, configPath, nil, headers)
		if err != nil || status >= 400 {
			recordResponse(configPath, raw, status, err, 0)
			continue
		}
		value, decodeErr := decodeJSONValue(raw)
		if decodeErr != nil {
			recordResponse(configPath, raw, status, decodeErr, 0)
			continue
		}
		catalog := codeBuddyModelCatalogFromValue(value)
		record(configPath, status, nil, len(catalog))
		if len(catalog) > 0 {
			discovered = mergeCodeBuddyModelCatalog(discovered, catalog)
		}
		if effectiveEnterpriseID == "" {
			if object, ok := value.(map[string]any); ok {
				effectiveEnterpriseID = codeBuddyEnterpriseIDFromObject(object)
				if effectiveEnterpriseID != "" {
					headers["X-Enterprise-Id"] = effectiveEnterpriseID
					headers["X-Tenant-Id"] = effectiveEnterpriseID
				}
			}
		}
	}
	if effectiveEnterpriseID != "" {
		raw, status, err := s.do(ctx, sess, http.MethodGet, codeBuddyEnterpriseModelsPath(effectiveEnterpriseID), nil, headers)
		if err == nil && status < 400 {
			if value, decodeErr := decodeJSONValue(raw); decodeErr == nil {
				if catalog := codeBuddyModelCatalogFromValue(value); len(catalog) > 0 {
					// The VS Code client classifies custom enterprise models by ID
					// after receiving this endpoint's top-level array.
					discovered = mergeCodeBuddyModelCatalog(discovered, markCodeBuddyEnterprise(catalog))
				}
				record(codeBuddyEnterpriseModelsPath(effectiveEnterpriseID), status, nil, len(codeBuddyModelCatalogFromValue(value)))
			} else {
				recordResponse(codeBuddyEnterpriseModelsPath(effectiveEnterpriseID), raw, status, decodeErr, 0)
			}
		} else {
			recordResponse(codeBuddyEnterpriseModelsPath(effectiveEnterpriseID), raw, status, err, 0)
		}
	}
	if len(discovered) > 0 {
		discovered = markCodeBuddyEnterprise(discovered)
		discovered = filterCodeBuddyCatalogForRegion(discovered, sess.Region)
		if len(discovered) > 0 {
			result.uid = effectiveUserID
			result.enterprise = effectiveEnterpriseID
			result.models, result.catalog, result.live = codeBuddyModelIDs(discovered), discovered, true
			return result
		}
	}
	// Older domestic tenants expose the model IDs through the plugin account
	// endpoint. Keep this compatibility path, but never mix in another region.
	if raw, status, err := s.do(ctx, sess, http.MethodGet, "/v2/plugin/accounts", nil, headers); err == nil && status < 400 {
		if value, decodeErr := decodeJSONValue(raw); decodeErr == nil {
			if catalog := codeBuddyModelCatalogFromValue(value); len(catalog) > 0 {
				catalog = filterCodeBuddyCatalogForRegion(catalog, sess.Region)
				if len(catalog) > 0 {
					result.uid = effectiveUserID
					result.enterprise = effectiveEnterpriseID
					ids := make([]string, 0, len(catalog))
					for _, model := range catalog {
						ids = append(ids, model.ID)
					}
					result.models, result.catalog, result.live = uniqueStrings(append([]string{"auto"}, ids...)), catalog, true
					return result
				} else {
					result.diagnostics = append(result.diagnostics, "/v2/plugin/accounts: models exist but none match region")
				}
			}
			record("/v2/plugin/accounts", status, nil, 0)
		} else {
			recordResponse("/v2/plugin/accounts", raw, status, decodeErr, 0)
		}
	} else {
		recordResponse("/v2/plugin/accounts", raw, status, err, 0)
	}
	ids := CodeBuddyModelsForRegion(sess.Region)
	result.models, result.catalog = ids, codeBuddyCatalogForIDs(ids)
	result.uid = effectiveUserID
	result.enterprise = effectiveEnterpriseID
	return result
}

// Keep these values aligned with the VS Code client used for compatibility.
// The upstream validates the product token in User-Agent (error 12403 when it
// cannot parse this value), and distinguishes IDE and plugin versions.
const (
	codeBuddyVSCodeVersion = "1.111.0"
	codeBuddyPluginVersion = "4.11.36344970"
	codeBuddyUserAgent     = "VSCode/" + codeBuddyVSCodeVersion + " CodeBuddy/" + codeBuddyPluginVersion
)

// codeBuddyPluginAccountFromValue mirrors the VS Code extension's account
// selection. /v2/plugin/accounts returns {data:{accounts:[...]}} and may
// contain multiple enterprises. Only plugin-enabled accounts are eligible;
// the last logged-in account is the active one.
func codeBuddyPluginAccountFromValue(value any) any {
	var accounts []any
	if object, ok := value.(map[string]any); ok {
		if data, ok := object["data"].(map[string]any); ok {
			accounts, _ = data["accounts"].([]any)
		}
		if len(accounts) == 0 {
			accounts, _ = object["accounts"].([]any)
		}
	}
	if len(accounts) == 0 {
		return value
	}
	var firstEnabled, lastLogin any
	for _, account := range accounts {
		object, ok := account.(map[string]any)
		if !ok {
			continue
		}
		if !codeBuddyBoolField(object, "pluginEnabled", "plugin_enabled") {
			continue
		}
		if firstEnabled == nil {
			firstEnabled = account
		}
		if codeBuddyBoolField(object, "lastLogin", "last_login") {
			lastLogin = account
			break
		}
		if lastLogin == nil {
			lastLogin = account
		}
	}
	if lastLogin != nil {
		return lastLogin
	}
	if firstEnabled != nil {
		return firstEnabled
	}
	return value
}

func (s *CodeBuddyOAuthService) fetchModels(ctx context.Context, sess *codeBuddySession, token, uid, enterpriseID string) []string {
	models, _, _ := s.fetchModelCatalog(ctx, sess, token, uid, enterpriseID)
	return models
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func codeBuddyDomain(endpoint string) string {
	endpoint = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(endpoint), "https://"), "http://")
	if i := strings.IndexByte(endpoint, '/'); i >= 0 {
		endpoint = endpoint[:i]
	}
	return endpoint
}

func codeBuddySessionDomain(sess *codeBuddySession) string {
	if sess != nil && strings.TrimSpace(sess.Domain) != "" {
		return strings.TrimSpace(sess.Domain)
	}
	if sess == nil {
		return ""
	}
	if strings.TrimSpace(sess.Region) != "" {
		return CodeBuddyAuthDomainForRegion(sess.Region)
	}
	return codeBuddyDomain(sess.Endpoint)
}

func (s *CodeBuddyOAuthService) Start(ctx context.Context, in CodeBuddyOAuthStartInput) (*CodeBuddyOAuthStartResult, error) {
	endpoint, err := CodeBuddyEndpointForRegion(in.Region)
	if err != nil {
		return nil, err
	}
	id, err := randomCodeBuddyID()
	if err != nil {
		return nil, err
	}
	state := &codeBuddySession{ID: id, AccountID: in.AccountID, Region: strings.ToLower(strings.TrimSpace(in.Region)), Endpoint: endpoint, Name: strings.TrimSpace(in.Name), Notes: strings.TrimSpace(in.Notes), ProxyID: in.ProxyID, GroupIDs: in.GroupIDs, Concurrency: in.Concurrency, LoadFactor: in.LoadFactor, Priority: in.Priority, RateMultiplier: in.RateMultiplier, AutoPauseOnExpired: in.AutoPauseOnExpired, ModelMapping: in.ModelMapping, ReferenceCostUnits: in.ReferenceCostUnits, ReferenceCredits: in.ReferenceCredits, TokensPerCredit: in.TokensPerCredit, AccountExpiresAt: in.ExpiresAt, SessionExpiresAt: time.Now().Add(codeBuddySessionTTL)}
	raw, status, err := s.do(ctx, state, http.MethodPost, "/v2/plugin/auth/state?platform=VSCode", []byte("{}"), map[string]string{
		"X-Domain":             codeBuddyDomain(endpoint),
		"X-Product":            "SaaS",
		"X-IDE-Name":           "VSCode",
		"X-Requested-With":     "XMLHttpRequest",
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	})
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("codebuddy auth state returned %d: %s", status, string(raw))
	}
	v, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	state.State = stringField(v, "state")
	authURL := stringField(v, "authUrl", "auth_url")
	if state.State == "" || authURL == "" {
		return nil, errors.New("codebuddy auth state response missing state or authUrl")
	}
	s.mu.Lock()
	s.sessions[id] = state
	s.mu.Unlock()
	return &CodeBuddyOAuthStartResult{SessionID: id, AuthURL: authURL, Region: state.Region, ExpiresAt: state.SessionExpiresAt.Unix()}, nil
}

func (s *CodeBuddyOAuthService) Poll(ctx context.Context, sessionID string) (*CodeBuddyOAuthPollResult, error) {
	s.mu.Lock()
	sess := s.sessions[sessionID]
	s.mu.Unlock()
	if sess == nil {
		return nil, errors.New("codebuddy oauth session not found")
	}
	if time.Now().After(sess.SessionExpiresAt) {
		return &CodeBuddyOAuthPollResult{Status: "expired", SessionID: sessionID, Message: "authorization session expired"}, nil
	}
	raw, status, err := s.do(ctx, sess, http.MethodGet, "/v2/plugin/auth/token?state="+sess.State, nil, map[string]string{
		"X-Domain":             codeBuddyDomain(sess.Endpoint),
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	})
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("codebuddy auth token returned %d: %s", status, string(raw))
	}
	if len(bytes.TrimSpace(raw)) == 0 || status == http.StatusNoContent {
		return &CodeBuddyOAuthPollResult{Status: "pending", SessionID: sessionID}, nil
	}
	v, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	token := stringField(v, "accessToken", "access_token", "token")
	if token == "" {
		return &CodeBuddyOAuthPollResult{Status: "pending", SessionID: sessionID}, nil
	}
	sess.AccessToken = token
	sess.RefreshToken = stringField(v, "refreshToken", "refresh_token")
	sess.Domain = stringField(v, "domain", "authDomain", "auth_domain")
	info, status, err := s.do(ctx, sess, http.MethodGet, "/v2/plugin/login/account?state="+sess.State, nil, map[string]string{"Authorization": "Bearer " + token, "X-Domain": codeBuddySessionDomain(sess), "X-No-User-Id": "true", "X-No-Enterprise-Id": "true", "X-No-Department-Info": "true"})
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("codebuddy account returned %d: %s", status, string(info))
	}
	accountInfo, _ := decodeObject(info)
	if sess.Domain == "" {
		sess.Domain = stringField(accountInfo, "domain", "authDomain", "auth_domain")
	}
	if sess.RefreshToken == "" {
		sess.RefreshToken = stringField(accountInfo, "refreshToken", "refresh_token")
	}
	// Do not treat a plugin-account record's generic `id` (which may be an
	// account row ID) as the user ID header. CodeBuddy exposes the user value
	// explicitly as uid/userId; omitting the header is safer than sending the
	// wrong identity and making configuration requests fail.
	uid := stringField(accountInfo, "uid", "userId", "user_id")
	enterpriseID := codeBuddyEnterpriseIDFromObject(accountInfo)
	// Fetch the catalog from the selected regional CodeBuddy endpoint with the
	// newly issued token. Catalog discovery is deliberately best-effort here:
	// OAuth has already completed and a temporary configuration outage must not
	// discard the account or force the user through authorization again. The
	// explicit model-sync endpoint remains strict and will report upstream
	// failures to the user.
	models, catalog, live := s.fetchModelCatalog(ctx, sess, token, uid, enterpriseID)
	modelSource := "upstream"
	if !live {
		modelSource = "fallback"
	}
	name := sess.Name
	if name == "" {
		name = stringField(accountInfo, "name", "email", "username")
		if name == "" {
			name = "CodeBuddy " + sess.Region
		}
	}
	creds := map[string]any{"access_token": token, "refresh_token": sess.RefreshToken, "uid": uid, "enterprise_id": enterpriseID, "domain": sess.Domain, "base_url": sess.Endpoint, "region": sess.Region, "platform": "VSCode", "ide_type": "VSCode", "platform_version": codeBuddyVSCodeVersion, "product_version": codeBuddyPluginVersion, "deployment_type": "SaaS", "model": "auto", "models": models, "model_catalog": catalog, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if len(sess.ModelMapping) > 0 {
		mapping := make(map[string]any, len(sess.ModelMapping))
		for key, value := range sess.ModelMapping {
			mapping[key] = value
		}
		creds["model_mapping"] = mapping
	}
	if validCodeBuddyPositiveNumber(sess.ReferenceCostUnits) && validCodeBuddyPositiveNumber(sess.ReferenceCredits) && validCodeBuddyPositiveNumber(sess.TokensPerCredit) {
		creds[CodeBuddyCredentialReferenceCostUnits] = sess.ReferenceCostUnits
		creds[CodeBuddyCredentialReferenceCredits] = sess.ReferenceCredits
		creds[CodeBuddyCredentialTokensPerCredit] = sess.TokensPerCredit
	}
	if sess.AccountID > 0 {
		updated, err := s.admin.UpdateAccount(ctx, sess.AccountID, &UpdateAccountInput{Type: AccountTypeOAuth, Credentials: creds, Extra: map[string]any{"codebuddy_region": sess.Region, "codebuddy_models": models, "codebuddy_model_catalog": catalog, "codebuddy_model_source": modelSource}})
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		return &CodeBuddyOAuthPollResult{Status: "completed", SessionID: sessionID, Account: updated}, nil
	}
	input := &CreateAccountInput{Name: name, Platform: PlatformCodeBuddy, Type: AccountTypeOAuth, Credentials: creds, Extra: map[string]any{"codebuddy_region": sess.Region, "codebuddy_models": models, "codebuddy_model_catalog": catalog, "codebuddy_model_source": modelSource}, ProxyID: sess.ProxyID, GroupIDs: sess.GroupIDs, Concurrency: sess.Concurrency, Priority: sess.Priority, LoadFactor: sess.LoadFactor, RateMultiplier: sess.RateMultiplier, AutoPauseOnExpired: sess.AutoPauseOnExpired, SkipDefaultGroupBind: false, SkipMixedChannelCheck: true}
	input.ExpiresAt = sess.AccountExpiresAt
	if sess.Notes != "" {
		input.Notes = &sess.Notes
	}
	created, err := s.admin.CreateAccount(ctx, input)
	if err != nil {
		return nil, err
	}
	sess.AccountID = created.ID
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	return &CodeBuddyOAuthPollResult{Status: "completed", SessionID: sessionID, Account: created}, nil
}

// Models returns the account-scoped model catalog when CodeBuddy exposes one.
// The built-in catalog is returned when the upstream does not provide a list.
func (s *CodeBuddyOAuthService) ModelsWithCatalog(ctx context.Context, account *Account) ([]string, []CodeBuddyModel, error) {
	if account == nil || account.Platform != PlatformCodeBuddy {
		return nil, nil, errors.New("codebuddy account required")
	}
	endpoint := strings.TrimSpace(account.GetCredential("base_url"))
	if endpoint == "" {
		endpoint = CodeBuddyDomesticEndpoint
	}
	region := CodeBuddyRegionForAccount(account)
	// The selected account region is authoritative. Do not let a stale or
	// manually edited base_url redirect a domestic account to the global host.
	endpoint, _ = CodeBuddyEndpointForRegion(region)
	token := strings.TrimSpace(account.GetCredential("access_token"))
	if token == "" {
		if catalog := CodeBuddyStoredModelCatalog(account); len(catalog) > 0 {
			catalog = filterCodeBuddyCatalogForRegion(catalog, region)
			ids := make([]string, 0, len(catalog))
			for _, model := range catalog {
				ids = append(ids, model.ID)
			}
			ids = uniqueStrings(append([]string{"auto"}, ids...))
			if len(ids) == 0 {
				return nil, nil, errors.New("codebuddy stored model catalog has no models for account region")
			}
			return ids, catalog, nil
		}
		return nil, nil, errors.New("codebuddy access token missing")
	}
	sess := &codeBuddySession{Endpoint: endpoint, Region: region, Domain: account.GetCredential("domain"), ProxyID: account.ProxyID}
	result := s.fetchModelCatalogDetailed(ctx, sess, token, account.GetCredential("uid"), account.GetCredential("enterprise_id"))
	if result.uid != "" || result.enterprise != "" {
		if account.Credentials == nil {
			account.Credentials = make(map[string]any)
		}
		if result.uid != "" {
			account.Credentials["uid"] = result.uid
		}
		if result.enterprise != "" {
			account.Credentials["enterprise_id"] = result.enterprise
		}
	}
	if !result.live {
		// The catalog endpoint is live-sync-only, but the account editor must
		// remain usable when CodeBuddy is temporarily unavailable. Reuse the
		// last successful, region-filtered catalog for reads; the explicit sync
		// endpoint below remains strict and still reports the upstream failure.
		if catalog := filterCodeBuddyCatalogForRegion(CodeBuddyStoredModelCatalog(account), region); len(catalog) > 0 {
			ids := make([]string, 0, len(catalog))
			for _, model := range catalog {
				ids = append(ids, model.ID)
			}
			return uniqueStrings(append([]string{"auto"}, ids...)), catalog, nil
		}
		diagnostics := strings.Join(uniqueStrings(result.diagnostics), "; ")
		if diagnostics == "" {
			diagnostics = "no usable model response"
		}
		return nil, nil, fmt.Errorf("codebuddy upstream model catalog is unavailable (region=%s endpoint=%s; %s)", region, endpoint, diagnostics)
	}
	return result.models, result.catalog, nil
}

// SyncModelsWithCatalog always reads the current catalog from CodeBuddy's
// regional upstream endpoint. Unlike ModelsWithCatalog, it never falls back to
// a previously persisted catalog, because a sync operation must be explicit
// about whether the upstream was reached successfully.
func (s *CodeBuddyOAuthService) SyncModelsWithCatalog(ctx context.Context, account *Account) ([]string, []CodeBuddyModel, error) {
	if account == nil || account.Platform != PlatformCodeBuddy {
		return nil, nil, errors.New("codebuddy account required")
	}
	endpoint := strings.TrimSpace(account.GetCredential("base_url"))
	if endpoint == "" {
		endpoint = CodeBuddyDomesticEndpoint
	}
	token := strings.TrimSpace(account.GetCredential("access_token"))
	if token == "" {
		return nil, nil, errors.New("codebuddy access token missing; authorize the account before syncing upstream models")
	}
	region := CodeBuddyRegionForAccount(account)
	endpoint, _ = CodeBuddyEndpointForRegion(region)
	sess := &codeBuddySession{Endpoint: endpoint, Region: region, Domain: account.GetCredential("domain"), ProxyID: account.ProxyID}
	result := s.fetchModelCatalogDetailed(ctx, sess, token, account.GetCredential("uid"), account.GetCredential("enterprise_id"))
	if result.uid != "" || result.enterprise != "" {
		if account.Credentials == nil {
			account.Credentials = make(map[string]any)
		}
		if result.uid != "" {
			account.Credentials["uid"] = result.uid
		}
		if result.enterprise != "" {
			account.Credentials["enterprise_id"] = result.enterprise
		}
	}
	if !result.live {
		diagnostics := strings.Join(uniqueStrings(result.diagnostics), "; ")
		if diagnostics == "" {
			diagnostics = "no usable model response"
		}
		return nil, nil, fmt.Errorf("codebuddy upstream model catalog is unavailable (region=%s endpoint=%s; %s)", region, endpoint, diagnostics)
	}
	return result.models, result.catalog, nil
}

func (s *CodeBuddyOAuthService) Models(ctx context.Context, account *Account) ([]string, error) {
	models, _, err := s.ModelsWithCatalog(ctx, account)
	return models, err
}

// CodeBuddyStoredModelCatalog reads the metadata persisted at OAuth creation.
func CodeBuddyStoredModelCatalog(account *Account) []CodeBuddyModel {
	if account == nil {
		return nil
	}
	var raw any
	if account.Extra != nil {
		raw = account.Extra["codebuddy_model_catalog"]
	}
	if raw == nil {
		raw = account.Credentials["model_catalog"]
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var catalog []CodeBuddyModel
	if err := json.Unmarshal(encoded, &catalog); err != nil {
		return nil
	}
	return catalog
}

// CodeBuddyModelCreditsMultiplier returns the raw upstream credit multiplier
// for the first matching model candidate. A catalogue value of zero is kept
// as zero; it is different from a missing multiplier and is handled by the
// local cost floor in CodeBuddyProviderCostMultiplier.
func CodeBuddyModelCreditsMultiplier(account *Account, models ...string) float64 {
	if account == nil || account.Platform != PlatformCodeBuddy {
		return 1
	}
	catalog := CodeBuddyStoredModelCatalog(account)
	if len(catalog) == 0 {
		return 1
	}
	for _, candidate := range models {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		for _, model := range catalog {
			if strings.ToLower(strings.TrimSpace(model.ID)) != candidate || model.CreditsMultiplier == nil {
				continue
			}
			return *model.CreditsMultiplier
		}
	}
	return 1
}

// CodeBuddyModelBillingMultiplier is retained for callers that need the
// catalogue value as a billable scalar. Request billing should use
// CodeBuddyProviderCostMultiplier so the value is normalized against the
// system's base token cost first.
func CodeBuddyModelBillingMultiplier(account *Account, models ...string) float64 {
	multiplier := CodeBuddyModelCreditsMultiplier(account, models...)
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return codeBuddyMinimumLocalMultiple
	}
	return multiplier
}

func (s *CodeBuddyOAuthService) Refresh(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || account.Platform != PlatformCodeBuddy {
		return nil, errors.New("codebuddy account required")
	}
	refresh := account.GetCredential("refresh_token")
	if refresh == "" {
		return nil, errors.New("codebuddy refresh token missing")
	}
	// The persisted region is authoritative. A stale or manually edited
	// base_url must not send a domestic account to the international host (or
	// vice versa), because CodeBuddy refresh tokens are regional.
	region := CodeBuddyRegionForAccount(account)
	endpoint, _ := CodeBuddyEndpointForRegion(region)
	sess := &codeBuddySession{Endpoint: endpoint, Region: region, Domain: account.GetCredential("domain"), ProxyID: account.ProxyID}
	// The VSCode extension posts an empty JSON object (rather than a form or
	// grant_type payload); keep the wire shape identical to its plugin client.
	raw, status, err := s.do(ctx, sess, http.MethodPost, "/v2/plugin/auth/token/refresh", []byte("{}"), map[string]string{"X-Refresh-Token": refresh, "X-Auth-Refresh-Source": "plugin", "X-Domain": codeBuddySessionDomain(sess), "X-Product": "SaaS", "X-IDE-Type": "VSCode", "X-IDE-Name": "VSCode", "X-IDE-Version": codeBuddyVSCodeVersion, "X-Product-Version": codeBuddyPluginVersion, "X-Requested-With": "XMLHttpRequest"})
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("codebuddy refresh returned %d: %s", status, string(raw))
	}
	v, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	access := stringField(v, "accessToken", "access_token", "token")
	nextRefresh := stringField(v, "refreshToken", "refresh_token")
	if access == "" {
		return nil, errors.New("codebuddy refresh response missing access token")
	}
	updates := map[string]any{"access_token": access, "expires_at": codeBuddyRefreshExpiry(v), "platform": "VSCode", "ide_type": "VSCode", "platform_version": codeBuddyVSCodeVersion, "product_version": codeBuddyPluginVersion, "deployment_type": "SaaS"}
	if domain := stringField(v, "domain", "authDomain", "auth_domain"); domain != "" {
		updates["domain"] = domain
	}
	if nextRefresh != "" {
		updates["refresh_token"] = nextRefresh
	}
	updated, err := s.admin.UpdateAccount(ctx, account.ID, &UpdateAccountInput{Credentials: updates})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func codeBuddyRefreshExpiry(v map[string]any) string {
	if expiry := stringField(v, "expiresAt", "expires_at", "expiredAt", "expired_at"); expiry != "" {
		return expiry
	}
	for _, key := range []string{"expiresIn", "expires_in", "expires"} {
		if raw, ok := v[key]; ok {
			switch n := raw.(type) {
			case float64:
				if n > 0 {
					return time.Now().Add(time.Duration(n) * time.Second).UTC().Format(time.RFC3339)
				}
			case json.Number:
				if seconds, err := n.Int64(); err == nil && seconds > 0 {
					return time.Now().Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339)
				}
			case string:
				if seconds, err := time.ParseDuration(strings.TrimSpace(n) + "s"); err == nil && seconds > 0 {
					return time.Now().Add(seconds).UTC().Format(time.RFC3339)
				}
			}
		}
	}
	return time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
}

// CodeBuddyTokenRefresher adapts CodeBuddy's provider-specific refresh API to
// the shared background OAuth refresh scheduler.
type CodeBuddyTokenRefresher struct{ oauth *CodeBuddyOAuthService }

func NewCodeBuddyTokenRefresher(oauth *CodeBuddyOAuthService) *CodeBuddyTokenRefresher {
	return &CodeBuddyTokenRefresher{oauth: oauth}
}

func (r *CodeBuddyTokenRefresher) CacheKey(account *Account) string {
	if account == nil {
		return "codebuddy:unknown"
	}
	return "codebuddy:" + fmt.Sprint(account.ID)
}

func (r *CodeBuddyTokenRefresher) CanRefresh(account *Account) bool {
	return r != nil && r.oauth != nil && account != nil && account.Platform == PlatformCodeBuddy && account.IsOAuth() && account.GetCredential("refresh_token") != ""
}

func (r *CodeBuddyTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil {
		return false
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	return expiresAt != nil && time.Until(*expiresAt) < refreshWindow
}

func (r *CodeBuddyTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	updated, err := r.oauth.Refresh(ctx, account)
	if err != nil {
		return nil, err
	}
	return MergeCredentials(account.Credentials, updated.Credentials), nil
}
