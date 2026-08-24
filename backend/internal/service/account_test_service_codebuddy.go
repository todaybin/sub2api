package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

// testCodeBuddyConnection probes the regional CodeBuddy Chat Completions
// endpoint. CodeBuddy accepts the OpenAI request shape but emits SSE, so the
// account test always asks the upstream for a stream and reuses the standard
// OpenAI SSE parser for the admin test events.
func (s *AccountTestService) testCodeBuddyConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	if account == nil {
		return s.sendErrorAndEnd(c, "CodeBuddy account is missing")
	}
	token := strings.TrimSpace(account.GetCredential("access_token"))
	if token == "" {
		return s.sendErrorAndEnd(c, "No CodeBuddy access token available")
	}

	model := strings.TrimSpace(modelID)
	if model == "" {
		model = "auto"
	}
	model = account.GetMappedModel(model)
	body := createOpenAIChatCompletionsTestPayload(model, prompt)
	// CodeBuddy rejects requests whose first message is not a system prompt
	// (error 11128). Keep this provider-specific requirement out of the
	// generic OpenAI test payload used by other providers.
	prependCodeBuddySystemPrompt(body)
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create CodeBuddy test payload")
	}

	endpoint, err := codeBuddyChatCompletionsURL(account)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create CodeBuddy request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	applyCodeBuddyHeaders(req.Header, account)
	applyCodeBuddyRequestHeaders(req.Header, c, bodyBytes)
	account.ApplyHeaderOverrides(req.Header)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 CodeBuddy /v2/chat/completions 测试连接"})

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	var profile *tlsfingerprint.Profile
	if s.tlsFPProfileService != nil {
		profile = s.tlsFPProfileService.ResolveTLSProfile(account)
	}
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, profile)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("CodeBuddy request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			_ = s.accountRepo.SetError(c.Request.Context(), account.ID, fmt.Sprintf("CodeBuddy authentication failed (401): %s", string(body)))
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("CodeBuddy API (/v2/chat/completions) returned %d: %s", resp.StatusCode, string(body)))
	}
	return s.processOpenAIChatCompletionsStream(c, resp.Body)
}

func prependCodeBuddySystemPrompt(payload map[string]any) {
	if messages, ok := payload["messages"].([]map[string]any); ok {
		payload["messages"] = append([]map[string]any{{
			"role":    "system",
			"content": "You are a helpful assistant.",
		}}, messages...)
	}
}

func codeBuddyChatCompletionsURL(account *Account) (string, error) {
	region := CodeBuddyRegionForAccount(account)
	expected, err := CodeBuddyEndpointForRegion(region)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
	if base == "" {
		base = expected
	}
	parsed, parseErr := url.Parse(base)
	if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid CodeBuddy base_url")
	}
	expectedURL, _ := url.Parse(expected)
	if !strings.EqualFold(parsed.Host, expectedURL.Host) {
		return "", fmt.Errorf("CodeBuddy %s account must use %s", region, expected)
	}
	return strings.TrimRight(base, "/") + "/v2/chat/completions", nil
}

func applyCodeBuddyHeaders(header http.Header, account *Account) {
	if uid := strings.TrimSpace(account.GetCredential("uid")); uid != "" {
		header.Set("X-User-Id", uid)
	}
	if enterprise := strings.TrimSpace(account.GetCredential("enterprise_id")); enterprise != "" {
		header.Set("X-Enterprise-Id", enterprise)
		header.Set("X-Tenant-Id", enterprise)
	}
	// The VS Code extension uses auth.domain, which is not necessarily the
	// hostname of the regional API endpoint. Keep that value when OAuth saved
	// it and only derive a fallback for older accounts.
	domain := strings.TrimSpace(account.GetCredential("domain"))
	if domain == "" {
		domain = CodeBuddyAuthDomainForRegion(CodeBuddyRegionForAccount(account))
	}
	if domain != "" {
		header.Set("X-Domain", domain)
	}
	header.Set("X-Product", firstNonEmptyCodeBuddyCredential(account, "deployment_type", "deploymentType", "SaaS"))
	header.Set("X-IDE-Type", firstNonEmptyCodeBuddyCredential(account, "ide_type", "ideType", "VSCode"))
	header.Set("X-IDE-Name", firstNonEmptyCodeBuddyCredential(account, "platform", "ide_name", "VSCode"))
	header.Set("X-IDE-Version", firstNonEmptyCodeBuddyCredential(account, "platform_version", "platformVersion", codeBuddyVSCodeVersion))
	header.Set("X-Product-Version", firstNonEmptyCodeBuddyCredential(account, "product_version", "productVersion", codeBuddyPluginVersion))
	// Match the extension's product user-agent. In particular, the domestic
	// configuration API returns error 12403 when it cannot identify it.
	header.Set("User-Agent", codeBuddyUserAgent)
	header.Set("X-Requested-With", "XMLHttpRequest")
}

func firstNonEmptyCodeBuddyCredential(account *Account, keys ...string) string {
	if account != nil {
		for _, key := range keys {
			if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
				return value
			}
		}
	}
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}
