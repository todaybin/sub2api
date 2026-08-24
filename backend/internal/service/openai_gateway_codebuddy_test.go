//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codeBuddyGatewayTestAccount() *Account {
	return &Account{ID: 7, Platform: PlatformCodeBuddy, Type: AccountTypeOAuth, Credentials: map[string]any{
		"access_token": "upstream-token",
		"base_url":     CodeBuddyDomesticEndpoint,
		"region":       CodeBuddyRegionDomestic,
		"domain":       "www.workbuddy.cn",
		"uid":          "uid-1",
	}}
}

func TestNormalizeOpenAICompatiblePlatformKeepsCodeBuddy(t *testing.T) {
	require.Equal(t, PlatformCodeBuddy, NormalizeOpenAICompatiblePlatform(PlatformCodeBuddy))
}

func TestCodeBuddyModelCatalogRestrictsAccountScheduling(t *testing.T) {
	domestic := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"models": []any{"auto", "hy3", "glm-5.3"},
	}}
	international := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"models": []any{"auto", "gpt-5.6-luna"},
	}}

	require.True(t, domestic.IsModelSupported("hy3"))
	require.False(t, domestic.IsModelSupported("gpt-5.6-luna"))
	require.False(t, international.IsModelSupported("hy3"))
	require.True(t, international.IsModelSupported("gpt-5.6-luna"))
}

func TestCodeBuddyModelMappingStillRequiresUpstreamCatalogSupport(t *testing.T) {
	account := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"models":        []string{"hy3"},
		"model_mapping": map[string]any{"fast": "hy3", "wrong": "gpt-5.6-luna"},
	}}

	require.True(t, account.IsModelSupported("fast"))
	require.False(t, account.IsModelSupported("wrong"))
	require.False(t, account.IsModelSupported("hy3"), "an explicit mapping remains a client-facing allowlist")
}

func codeBuddyTestSSE(model, content string) string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl-cb","object":"chat.completion.chunk","created":1,"model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"` + content + `"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-cb","object":"chat.completion.chunk","created":1,"model":"` + model + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`data: [DONE]`,
	}, "\n") + "\n"
}

func TestCodeBuddyChatCompletionsURLKeepsAccountRegion(t *testing.T) {
	domestic := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"region":   CodeBuddyRegionDomestic,
		"base_url": CodeBuddyDomesticEndpoint,
	}}
	url, err := codeBuddyChatCompletionsURL(domestic)
	require.NoError(t, err)
	require.Equal(t, "https://copilot.tencent.com/v2/chat/completions", url)

	wrongRegion := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"region":   CodeBuddyRegionDomestic,
		"base_url": CodeBuddyInternationalEndpoint,
	}}
	_, err = codeBuddyChatCompletionsURL(wrongRegion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must use https://copilot.tencent.com")
}

func TestApplyCodeBuddyHeaders(t *testing.T) {
	account := &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"base_url":      CodeBuddyDomesticEndpoint,
		"uid":           "uid-1",
		"enterprise_id": "enterprise-1",
		"domain":        "auth.example.com",
	}}
	header := make(http.Header)
	applyCodeBuddyHeaders(header, account)
	require.Equal(t, "uid-1", header.Get("X-User-Id"))
	require.Equal(t, "enterprise-1", header.Get("X-Enterprise-Id"))
	require.Equal(t, "enterprise-1", header.Get("X-Tenant-Id"))
	require.Equal(t, "auth.example.com", header.Get("X-Domain"))
	require.Equal(t, "VSCode", header.Get("X-IDE-Type"))
	require.Equal(t, codeBuddyVSCodeVersion, header.Get("X-IDE-Version"))
	require.Equal(t, codeBuddyPluginVersion, header.Get("X-Product-Version"))
	require.Equal(t, codeBuddyUserAgent, header.Get("User-Agent"))
	require.Equal(t, "VSCode", header.Get("X-IDE-Name"))
}

func TestApplyCodeBuddyHeadersUsesRegionalAuthDomainFallback(t *testing.T) {
	header := make(http.Header)
	applyCodeBuddyHeaders(header, &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"region": CodeBuddyRegionDomestic,
	}})
	require.Equal(t, CodeBuddyDomesticAuthDomain, header.Get("X-Domain"))

	header = make(http.Header)
	applyCodeBuddyHeaders(header, &Account{Platform: PlatformCodeBuddy, Credentials: map[string]any{
		"region": CodeBuddyRegionInternational,
	}})
	require.Equal(t, CodeBuddyGlobalAuthDomain, header.Get("X-Domain"))
}

func TestForceCodeBuddyStream(t *testing.T) {
	body, err := forceCodeBuddyStream([]byte(`{"model":"hy3","stream":false}`))
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.True(t, gjson.GetBytes(body, "stream_options.include_usage").Bool())
}

func TestApplyCodeBuddyRequestHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("X-Conversation-ID", "conversation-1")
	c.Request.Header.Set("X-Agent-Intent", "ask")
	header := make(http.Header)
	applyCodeBuddyRequestHeaders(header, c, []byte(`{"model":"hy3"}`))
	require.Equal(t, "ask", header.Get("X-Agent-Intent"))
	require.Equal(t, "conversation-1", header.Get("X-Conversation-ID"))
	require.NotEmpty(t, header.Get("X-Conversation-Request-ID"))
	require.Equal(t, header.Get("X-Conversation-Request-ID"), header.Get("X-Request-ID"))
	require.NotEmpty(t, header.Get("X-Conversation-Message-ID"))
	require.Equal(t, "hy3", header.Get("X-Model-ID"))
}

func TestApplyCodeBuddyRequestHeadersGeneratesConversationID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	header := make(http.Header)
	applyCodeBuddyRequestHeaders(header, c, []byte(`{"model":"hy3"}`))
	require.NotEmpty(t, header.Get("X-Conversation-ID"))
	require.Equal(t, "craft", header.Get("X-Agent-Intent"))
}

func TestEnsureCodeBuddySystemPrompt(t *testing.T) {
	body, err := ensureCodeBuddySystemPrompt([]byte(`{"model":"hy3","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.Equal(t, "system", gjson.GetBytes(body, "messages.0.role").String())
	require.Equal(t, "You are a helpful assistant.", gjson.GetBytes(body, "messages.0.content").String())
	require.Equal(t, "user", gjson.GetBytes(body, "messages.1.role").String())

	unchanged, err := ensureCodeBuddySystemPrompt([]byte(`{"messages":[{"role":"system","content":"custom"}]}`))
	require.NoError(t, err)
	require.Equal(t, "custom", gjson.GetBytes(unchanged, "messages.0.content").String())
}

func TestBufferCodeBuddyChatCompletionsAggregatesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"hy3","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"hy3","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"hy3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"hy3","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`data: [DONE]`,
	}, "\n") + "\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}
	account := &Account{ID: 7, Platform: PlatformCodeBuddy, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}

	result, err := svc.bufferCodeBuddyChatCompletions(c, resp, account, "hy3", "hy3", "hy3", nil, nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, "Hello", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
	require.Equal(t, "stop", gjson.Get(recorder.Body.String(), "choices.0.finish_reason").String())
	require.Equal(t, int64(5), gjson.Get(recorder.Body.String(), "usage.total_tokens").Int())
}

func TestCodeBuddyResponsesNonStreamingUsesSSEUpstreamAndReturnsResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"hy3","input":"hello","stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(codeBuddyTestSSE("hy3", "pong"))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.Forward(context.Background(), c, codeBuddyGatewayTestAccount(), body)
	require.NoError(t, err)
	require.False(t, result.Stream)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Equal(t, "www.workbuddy.cn", upstream.lastReq.Header.Get("X-Domain"))
	require.Equal(t, codeBuddyPluginVersion, upstream.lastReq.Header.Get("X-Product-Version"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "no-cache", upstream.lastReq.Header.Get("Cache-Control"))
	require.Equal(t, "identity", upstream.lastReq.Header.Get("Accept-Encoding"))
	require.Equal(t, "response", gjson.Get(recorder.Body.String(), "object").String())
	require.Equal(t, "pong", gjson.Get(recorder.Body.String(), "output.0.content.0.text").String())
}

func TestCodeBuddyAnthropicNonStreamingUsesSSEUpstreamAndReturnsMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"hy3","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(codeBuddyTestSSE("hy3", "pong"))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, codeBuddyGatewayTestAccount(), body, "", "")
	require.NoError(t, err)
	require.False(t, result.Stream)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Equal(t, "assistant", gjson.Get(recorder.Body.String(), "role").String())
	require.Equal(t, "pong", gjson.Get(recorder.Body.String(), "content.0.text").String())
}
