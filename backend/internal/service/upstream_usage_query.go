package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

const (
	UpstreamBillingBalanceProbeEnabledExtraKey = "upstream_billing_balance_probe_enabled"
	UpstreamBillingUsageQueryConfigExtraKey    = "upstream_billing_usage_query_config"
	UpstreamBillingUsageQuerySecretsKey        = "upstream_billing_usage_query_secrets"

	upstreamUsageQueryConfigMaxBytes = 32 * 1024
)

var upstreamUsageVariableNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

type UpstreamUsageQueryVariable struct {
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type UpstreamUsageQueryRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

type UpstreamUsageQueryMappingRule struct {
	Path       string  `json:"path,omitempty"`
	Default    any     `json:"default,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
	Divisor    float64 `json:"divisor,omitempty"`
	Derive     string  `json:"derive,omitempty"`
}

type UpstreamUsageQueryMapping struct {
	IsValid        *UpstreamUsageQueryMappingRule `json:"is_valid,omitempty"`
	InvalidMessage *UpstreamUsageQueryMappingRule `json:"invalid_message,omitempty"`
	PlanName       *UpstreamUsageQueryMappingRule `json:"plan_name,omitempty"`
	Remaining      *UpstreamUsageQueryMappingRule `json:"remaining,omitempty"`
	Used           *UpstreamUsageQueryMappingRule `json:"used,omitempty"`
	Total          *UpstreamUsageQueryMappingRule `json:"total,omitempty"`
	Unit           *UpstreamUsageQueryMappingRule `json:"unit,omitempty"`
	Extra          *UpstreamUsageQueryMappingRule `json:"extra,omitempty"`
}

// UpstreamUsageQueryConfig is deliberately data-only. It never executes code.
type UpstreamUsageQueryConfig struct {
	Version      int                                   `json:"version"`
	TemplateType string                                `json:"template_type"`
	ResultType   string                                `json:"result_type"`
	Variables    map[string]UpstreamUsageQueryVariable `json:"variables,omitempty"`
	Request      UpstreamUsageQueryRequest             `json:"request"`
	Mapping      UpstreamUsageQueryMapping             `json:"mapping"`
}

type UpstreamUsageQueryNormalizedResult struct {
	IsValid        bool           `json:"is_valid"`
	InvalidMessage string         `json:"invalid_message,omitempty"`
	PlanName       string         `json:"plan_name,omitempty"`
	Remaining      *float64       `json:"remaining,omitempty"`
	Used           *float64       `json:"used,omitempty"`
	Total          *float64       `json:"total,omitempty"`
	Unit           string         `json:"unit,omitempty"`
	Extra          map[string]any `json:"extra,omitempty"`
}

type UpstreamUsageQueryTestInput struct {
	Platform    string                    `json:"platform"`
	BaseURL     string                    `json:"base_url"`
	APIKey      string                    `json:"api_key"`
	AccessToken string                    `json:"access_token"`
	UserID      string                    `json:"user_id"`
	Variables   map[string]string         `json:"variables,omitempty"`
	Config      *UpstreamUsageQueryConfig `json:"config"`
}

type UpstreamUsageQueryTestResult struct {
	Result     UpstreamUsageQueryNormalizedResult `json:"result"`
	StatusCode int                                `json:"status_code"`
	Response   any                                `json:"response"`
}

func GenericUpstreamUsageQueryTemplate() *UpstreamUsageQueryConfig {
	return &UpstreamUsageQueryConfig{
		Version: 1, TemplateType: "generic", ResultType: "balance",
		Request: UpstreamUsageQueryRequest{
			URL: "{{rootUrl}}/user/balance", Method: http.MethodGet,
			Headers: map[string]string{"Authorization": "Bearer {{apiKey}}", "User-Agent": "cc-switch/1.0"},
		},
		Mapping: UpstreamUsageQueryMapping{
			IsValid:   &UpstreamUsageQueryMappingRule{Default: true},
			Remaining: &UpstreamUsageQueryMappingRule{Path: "balance"},
			Unit:      &UpstreamUsageQueryMappingRule{Path: "currency", Default: "USD"},
		},
	}
}

func NewAPIUpstreamUsageQueryTemplate() *UpstreamUsageQueryConfig {
	return &UpstreamUsageQueryConfig{
		Version: 1, TemplateType: "new_api", ResultType: "balance",
		Request: UpstreamUsageQueryRequest{
			URL: "{{rootUrl}}/api/user/self", Method: http.MethodGet,
			Headers: map[string]string{
				"Content-Type": "application/json", "Authorization": "Bearer {{accessToken}}",
				"User-Agent": "cc-switch/1.0", "New-Api-User": "{{userId}}",
			},
		},
		Mapping: UpstreamUsageQueryMapping{
			IsValid:        &UpstreamUsageQueryMappingRule{Path: "success", Default: false},
			InvalidMessage: &UpstreamUsageQueryMappingRule{Path: "message", Default: "query failed"},
			PlanName:       &UpstreamUsageQueryMappingRule{Path: "data.group", Default: "default"},
			Remaining:      &UpstreamUsageQueryMappingRule{Path: "data.quota", Divisor: 500000},
			Used:           &UpstreamUsageQueryMappingRule{Path: "data.used_quota", Divisor: 500000},
			Total:          &UpstreamUsageQueryMappingRule{Derive: "remaining_plus_used"},
			Unit:           &UpstreamUsageQueryMappingRule{Default: "USD"},
		},
	}
}

func DecodeUpstreamUsageQueryConfig(raw any) (*UpstreamUsageQueryConfig, error) {
	if raw == nil {
		return nil, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, invalidUpstreamUsageQueryConfig(err.Error())
	}
	if len(payload) > upstreamUsageQueryConfigMaxBytes {
		return nil, invalidUpstreamUsageQueryConfig("config exceeds 32KB")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var config UpstreamUsageQueryConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, invalidUpstreamUsageQueryConfig(err.Error())
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, invalidUpstreamUsageQueryConfig("config must contain one JSON object")
	}
	if err := ValidateUpstreamUsageQueryConfig(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

func ValidateUpstreamUsageQueryConfig(config *UpstreamUsageQueryConfig) error {
	if config == nil {
		return invalidUpstreamUsageQueryConfig("config is required")
	}
	if config.Version != 1 {
		return invalidUpstreamUsageQueryConfig("version must be 1")
	}
	switch config.TemplateType {
	case "generic", "new_api", "custom":
	default:
		return invalidUpstreamUsageQueryConfig("template_type must be generic, new_api, or custom")
	}
	if config.ResultType != "balance" && config.ResultType != "subscription" {
		return invalidUpstreamUsageQueryConfig("result_type must be balance or subscription")
	}
	method := strings.ToUpper(strings.TrimSpace(config.Request.Method))
	if method != http.MethodGet && method != http.MethodPost {
		return invalidUpstreamUsageQueryConfig("request.method must be GET or POST")
	}
	if strings.TrimSpace(config.Request.URL) == "" {
		return invalidUpstreamUsageQueryConfig("request.url is required")
	}
	for name := range config.Variables {
		if !upstreamUsageVariableNamePattern.MatchString(name) {
			return invalidUpstreamUsageQueryConfig("variable names must match [A-Za-z][A-Za-z0-9_]{0,63}")
		}
		switch name {
		case "baseUrl", "rootUrl", "apiKey", "accessToken", "userId":
			return invalidUpstreamUsageQueryConfig("built-in variable names cannot be redefined")
		}
	}
	for _, rule := range []*UpstreamUsageQueryMappingRule{
		config.Mapping.IsValid, config.Mapping.InvalidMessage, config.Mapping.PlanName,
		config.Mapping.Remaining, config.Mapping.Used, config.Mapping.Total, config.Mapping.Unit, config.Mapping.Extra,
	} {
		if rule == nil {
			continue
		}
		if rule.Divisor < 0 || rule.Multiplier < 0 {
			return invalidUpstreamUsageQueryConfig("mapping multiplier and divisor cannot be negative")
		}
		if rule.Derive != "" && rule.Derive != "remaining_plus_used" && rule.Derive != "total_minus_used" {
			return invalidUpstreamUsageQueryConfig("mapping derive is not supported")
		}
	}
	return nil
}

func invalidUpstreamUsageQueryConfig(message string) error {
	return infraerrors.BadRequest("INVALID_UPSTREAM_USAGE_QUERY_CONFIG", message)
}

// NormalizeUpstreamUsageQueryExtra validates account-owned query settings and
// materializes preset modes as complete JSON so they remain editable later.
func NormalizeUpstreamUsageQueryExtra(extra map[string]any) (map[string]any, error) {
	if extra == nil {
		return nil, nil
	}
	if raw, ok := extra[UpstreamBillingBalanceProbeEnabledExtraKey]; ok {
		if _, valid := raw.(bool); !valid {
			return nil, infraerrors.BadRequest("INVALID_UPSTREAM_BALANCE_PROBE_ENABLED", "upstream_billing_balance_probe_enabled must be a boolean")
		}
	}
	rawMode, hasMode := extra[UpstreamBillingBalanceQueryModeExtraKey]
	mode, _ := rawMode.(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
		if hasMode {
			extra[UpstreamBillingBalanceQueryModeExtraKey] = mode
		}
	}
	if mode != "auto" && mode != "generic" && mode != "new_api" && mode != "custom" {
		return nil, infraerrors.BadRequest("INVALID_UPSTREAM_BALANCE_QUERY_MODE", "upstream_billing_balance_query_mode must be auto, generic, new_api, or custom")
	}
	rawConfig, hasConfig := extra[UpstreamBillingUsageQueryConfigExtraKey]
	if !hasConfig || rawConfig == nil {
		switch mode {
		case "generic":
			extra[UpstreamBillingUsageQueryConfigExtraKey] = GenericUpstreamUsageQueryTemplate()
		case "new_api":
			extra[UpstreamBillingUsageQueryConfigExtraKey] = NewAPIUpstreamUsageQueryTemplate()
		case "custom":
			return nil, invalidUpstreamUsageQueryConfig("custom mode requires upstream_billing_usage_query_config")
		}
		return extra, nil
	}
	config, err := DecodeUpstreamUsageQueryConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	if mode != "auto" && config.TemplateType != mode && mode != "custom" {
		return nil, invalidUpstreamUsageQueryConfig("template_type must match upstream_billing_balance_query_mode")
	}
	extra[UpstreamBillingUsageQueryConfigExtraKey] = config
	return extra, nil
}

// ProtectUpstreamUsageQuerySecrets moves secret variable values out of the
// non-sensitive account extra JSON before persistence.
func ProtectUpstreamUsageQuerySecrets(extra, credentials map[string]any) error {
	if extra == nil || credentials == nil {
		return nil
	}
	raw, ok := extra[UpstreamBillingUsageQueryConfigExtraKey]
	if !ok || raw == nil {
		return nil
	}
	config, err := DecodeUpstreamUsageQueryConfig(raw)
	if err != nil {
		return err
	}
	secrets := make(map[string]any)
	if existing, ok := credentials[UpstreamBillingUsageQuerySecretsKey].(map[string]any); ok {
		for name, value := range existing {
			secrets[name] = value
		}
	}
	for name, variable := range config.Variables {
		if !variable.Secret || variable.Value == "" {
			continue
		}
		secrets[name] = variable.Value
		variable.Value = ""
		config.Variables[name] = variable
	}
	if len(secrets) > 0 {
		credentials[UpstreamBillingUsageQuerySecretsKey] = secrets
	}
	extra[UpstreamBillingUsageQueryConfigExtraKey] = config
	return nil
}

func upstreamUsageConfigForAccount(account *Account, mode string) (*UpstreamUsageQueryConfig, error) {
	if account != nil && account.Extra != nil {
		if raw, ok := account.Extra[UpstreamBillingUsageQueryConfigExtraKey]; ok && raw != nil {
			return DecodeUpstreamUsageQueryConfig(raw)
		}
	}
	switch mode {
	case "generic":
		return GenericUpstreamUsageQueryTemplate(), nil
	case "new_api":
		return NewAPIUpstreamUsageQueryTemplate(), nil
	default:
		return nil, invalidUpstreamUsageQueryConfig("custom mode requires upstream_billing_usage_query_config")
	}
}

func usageQueryVariables(account *Account, config *UpstreamUsageQueryConfig, overrides map[string]string) map[string]string {
	baseURL := ""
	apiKey := ""
	accessToken := ""
	userID := ""
	if account != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
		apiKey = account.GetCredential("api_key")
		accessToken = account.GetCredential(UpstreamBillingBalanceAccessTokenKey)
		userID = account.GetCredential(UpstreamBillingBalanceUserIDKey)
	}
	values := map[string]string{
		"baseUrl": baseURL, "rootUrl": strings.TrimRight(buildUpstreamBillingAuxiliaryURL(baseURL, "/"), "/"),
		"apiKey": apiKey, "accessToken": accessToken, "userId": userID,
	}
	secrets := map[string]any{}
	if account != nil && account.Credentials != nil {
		if raw, ok := account.Credentials[UpstreamBillingUsageQuerySecretsKey].(map[string]any); ok {
			secrets = raw
		}
	}
	for name, variable := range config.Variables {
		if variable.Value != "" {
			values[name] = variable.Value
		}
		if variable.Secret {
			if value, ok := secrets[name].(string); ok {
				values[name] = value
			}
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	return values
}

func interpolateUsageString(value string, variables map[string]string) string {
	for name, replacement := range variables {
		value = strings.ReplaceAll(value, "{{"+name+"}}", replacement)
	}
	return value
}

func interpolateUsageJSON(value any, variables map[string]string) any {
	switch typed := value.(type) {
	case string:
		return interpolateUsageString(typed, variables)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = interpolateUsageJSON(typed[i], variables)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = interpolateUsageJSON(item, variables)
		}
		return out
	default:
		return value
	}
}

func sameUsageQueryOrigin(baseURL, requestURL string) bool {
	base, baseErr := url.Parse(baseURL)
	target, targetErr := url.Parse(requestURL)
	if baseErr != nil || targetErr != nil || base.Scheme == "" || target.Scheme == "" {
		return false
	}
	return strings.EqualFold(base.Scheme, target.Scheme) && strings.EqualFold(base.Host, target.Host)
}

func (s *UpstreamBillingProbeService) executeUsageQuery(ctx context.Context, account *Account, config *UpstreamUsageQueryConfig, overrides map[string]string) (*UpstreamUsageQueryTestResult, upstreamBillingHTTPResult) {
	if err := ValidateUpstreamUsageQueryConfig(config); err != nil {
		return nil, upstreamBillingHTTPResult{reason: "invalid_query_config"}
	}
	variables := usageQueryVariables(account, config, overrides)
	requestURL := interpolateUsageString(config.Request.URL, variables)
	baseURL := variables["baseUrl"]
	if !sameUsageQueryOrigin(baseURL, requestURL) {
		return nil, upstreamBillingHTTPResult{reason: "query_url_origin_mismatch"}
	}
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return nil, upstreamBillingHTTPResult{reason: "request_build_failed"}
	}
	query := parsedURL.Query()
	for key, value := range config.Request.Query {
		query.Set(key, interpolateUsageString(value, variables))
	}
	parsedURL.RawQuery = query.Encode()
	var body io.Reader
	if config.Request.Body != nil {
		payload, marshalErr := json.Marshal(interpolateUsageJSON(config.Request.Body, variables))
		if marshalErr != nil {
			return nil, upstreamBillingHTTPResult{reason: "request_build_failed"}
		}
		body = bytes.NewReader(payload)
	}
	probeCtx, cancel := context.WithTimeout(ctx, upstreamBillingProbeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, strings.ToUpper(config.Request.Method), parsedURL.String(), body)
	if err != nil {
		return nil, upstreamBillingHTTPResult{reason: "request_build_failed"}
	}
	reqCtx := WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileDefault)
	if account.Platform == PlatformOpenAI {
		reqCtx = WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI)
	}
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(reqCtx))
	req.Header.Set("Accept", "application/json")
	for key, value := range config.Request.Headers {
		value = interpolateUsageString(value, variables)
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	proxyURL := ""
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
			return nil, upstreamBillingHTTPResult{reason: "proxy_unavailable"}
		}
		proxyURL = account.Proxy.URL()
	}
	var tlsProfile *tlsfingerprint.Profile
	if s.accountTestService.tlsFPProfileService != nil {
		tlsProfile = s.accountTestService.tlsFPProfileService.ResolveTLSProfile(account)
	}
	resp, err := s.accountTestService.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
	if err != nil {
		return nil, upstreamBillingHTTPResult{reason: "request_failed"}
	}
	if resp == nil || resp.Body == nil {
		return nil, upstreamBillingHTTPResult{reason: "empty_response"}
	}
	defer func() { _ = resp.Body.Close() }()
	httpResult := upstreamBillingHTTPResult{statusCode: resp.StatusCode, retryAfter: retryAfter(resp.Header, s.currentTime().UTC())}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, upstreamBillingProbeMaxBodyBytes+1))
	if err != nil {
		httpResult.reason = "response_read_failed"
		return nil, httpResult
	}
	if len(payload) > upstreamBillingProbeMaxBodyBytes {
		httpResult.reason = "response_too_large"
		return nil, httpResult
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		httpResult.reason = "http_error"
		return nil, httpResult
	}
	if !json.Valid(payload) {
		httpResult.reason = "invalid_response"
		return nil, httpResult
	}
	normalized, err := mapUsageQueryResponse(payload, config)
	if err != nil {
		httpResult.reason = "invalid_response"
		return nil, httpResult
	}
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		httpResult.reason = "invalid_response"
		return nil, httpResult
	}
	httpResult.body = payload
	return &UpstreamUsageQueryTestResult{Result: normalized, StatusCode: resp.StatusCode, Response: raw}, httpResult
}

func mapUsageQueryResponse(payload []byte, config *UpstreamUsageQueryConfig) (UpstreamUsageQueryNormalizedResult, error) {
	result := UpstreamUsageQueryNormalizedResult{IsValid: true}
	result.IsValid = mappedBool(payload, config.Mapping.IsValid, true)
	result.InvalidMessage = mappedString(payload, config.Mapping.InvalidMessage)
	result.PlanName = mappedString(payload, config.Mapping.PlanName)
	result.Remaining = mappedNumber(payload, config.Mapping.Remaining)
	result.Used = mappedNumber(payload, config.Mapping.Used)
	result.Total = mappedNumber(payload, config.Mapping.Total)
	if config.Mapping.Total != nil && config.Mapping.Total.Derive == "remaining_plus_used" && result.Remaining != nil && result.Used != nil {
		value := *result.Remaining + *result.Used
		result.Total = &value
	}
	if config.Mapping.Remaining != nil && config.Mapping.Remaining.Derive == "total_minus_used" && result.Total != nil && result.Used != nil {
		value := *result.Total - *result.Used
		result.Remaining = &value
	}
	result.Unit = mappedString(payload, config.Mapping.Unit)
	if result.Unit == "" && config.ResultType == "balance" {
		result.Unit = "USD"
	}
	if config.Mapping.Extra != nil && config.Mapping.Extra.Path != "" {
		value := gjson.GetBytes(payload, config.Mapping.Extra.Path)
		if value.Exists() {
			if object, ok := value.Value().(map[string]any); ok {
				result.Extra = object
			}
		}
	}
	return result, nil
}

func mappedJSONValue(payload []byte, rule *UpstreamUsageQueryMappingRule) any {
	if rule == nil {
		return nil
	}
	if rule.Path != "" {
		value := gjson.GetBytes(payload, rule.Path)
		if value.Exists() {
			return value.Value()
		}
	}
	return rule.Default
}

func mappedString(payload []byte, rule *UpstreamUsageQueryMappingRule) string {
	value := mappedJSONValue(payload, rule)
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func mappedBool(payload []byte, rule *UpstreamUsageQueryMappingRule, fallback bool) bool {
	value := mappedJSONValue(payload, rule)
	if value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(typed)
		return err == nil && parsed
	default:
		return fallback
	}
}

func mappedNumber(payload []byte, rule *UpstreamUsageQueryMappingRule) *float64 {
	if rule == nil || rule.Derive != "" {
		return nil
	}
	value := mappedJSONValue(payload, rule)
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	multiplier := rule.Multiplier
	if multiplier == 0 {
		multiplier = 1
	}
	divisor := rule.Divisor
	if divisor == 0 {
		divisor = 1
	}
	valueNumber := number * multiplier / divisor
	return &valueNumber
}

func usageQueryResultData(result *UpstreamUsageQueryTestResult, config *UpstreamUsageQueryConfig) map[string]any {
	data := map[string]any{"billing_mode": config.ResultType, "balance_source": config.TemplateType}
	if result.Result.PlanName != "" {
		data["plan_name"] = result.Result.PlanName
	}
	if result.Result.Remaining != nil {
		data["balance"] = *result.Result.Remaining
	}
	if result.Result.Used != nil {
		data["used"] = *result.Result.Used
	}
	if result.Result.Total != nil {
		data["total"] = *result.Result.Total
	}
	if result.Result.Unit != "" {
		data["currency"] = strings.ToUpper(result.Result.Unit)
	}
	if result.Result.Extra != nil {
		data["usage_query_extra"] = result.Result.Extra
	}
	return data
}

func (s *UpstreamBillingProbeService) TestUsageQuery(ctx context.Context, input *UpstreamUsageQueryTestInput) (*UpstreamUsageQueryTestResult, error) {
	if s == nil || s.accountTestService == nil || s.accountTestService.httpUpstream == nil {
		return nil, ErrUpstreamBillingProbeUnavailable
	}
	if input == nil || input.Config == nil {
		return nil, invalidUpstreamUsageQueryConfig("config is required")
	}
	if err := ValidateUpstreamUsageQueryConfig(input.Config); err != nil {
		return nil, err
	}
	normalizedBaseURL, err := s.accountTestService.validateUpstreamBaseURL(strings.TrimSpace(input.BaseURL))
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_UPSTREAM_USAGE_QUERY_BASE_URL", err.Error())
	}
	account := &Account{Platform: input.Platform, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"base_url": normalizedBaseURL, "api_key": input.APIKey,
		UpstreamBillingBalanceAccessTokenKey: input.AccessToken, UpstreamBillingBalanceUserIDKey: input.UserID,
	}}
	result, httpResult := s.executeUsageQuery(ctx, account, input.Config, input.Variables)
	if httpResult.reason != "" {
		return nil, infraerrors.BadRequest("UPSTREAM_USAGE_QUERY_TEST_FAILED", httpResult.reason)
	}
	return result, nil
}
