package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// ensureCodeBuddySystemPrompt mirrors the VS Code/converter behavior. The
// CodeBuddy upstream rejects a request whose first message is not system,
// while OpenAI-compatible clients are allowed to start with user content.
func ensureCodeBuddySystemPrompt(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, err
	}
	rawMessages, ok := payload["messages"].([]any)
	if !ok || len(rawMessages) == 0 {
		return body, nil
	}
	if first, ok := rawMessages[0].(map[string]any); ok {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(first["role"])), "system") {
			return body, nil
		}
	}
	payload["messages"] = append([]any{map[string]any{
		"role":    "system",
		"content": "You are a helpful assistant.",
	}}, rawMessages...)
	return json.Marshal(payload)
}

// applyCodeBuddyRequestHeaders mirrors the reserved headers assembled by the
// VS Code model client. OpenCode/CCS callers often omit them, so generate the
// per-request IDs that the plugin normally creates while preserving any
// stable conversation ID supplied by the client.
func applyCodeBuddyRequestHeaders(header http.Header, c *gin.Context, body []byte) {
	if header == nil {
		return
	}
	get := func(name string) string {
		if c == nil {
			return ""
		}
		return strings.TrimSpace(c.GetHeader(name))
	}
	copyHeader := func(name string) {
		if value := get(name); value != "" {
			header.Set(name, value)
		}
	}
	copyHeader("X-Agent-Intent")
	if header.Get("X-Agent-Intent") == "" {
		header.Set("X-Agent-Intent", "craft")
	}
	conversationID := get("X-Conversation-ID")
	if conversationID == "" {
		conversationID = strings.TrimSpace(gjson.GetBytes(body, "conversation_id").String())
	}
	if conversationID == "" && c != nil {
		conversationID = explicitOpenAISessionID(c, body)
	}
	if conversationID == "" {
		conversationID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	header.Set("X-Conversation-ID", conversationID)
	requestID := get("X-Conversation-Request-ID")
	if requestID == "" {
		requestID = get("X-Request-ID")
	}
	if requestID == "" {
		requestID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	header.Set("X-Conversation-Request-ID", requestID)
	header.Set("X-Request-ID", requestID)
	messageID := get("X-Conversation-Message-ID")
	if messageID == "" {
		messageID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	header.Set("X-Conversation-Message-ID", messageID)
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" && get("X-Model-ID") == "" {
		header.Set("X-Model-ID", model)
	}
}

// bufferCodeBuddyChatCompletions consumes CodeBuddy's mandatory SSE response
// and exposes the non-streaming OpenAI Chat Completions response requested by
// the downstream client.
func (s *OpenAIGatewayService) bufferCodeBuddyChatCompletions(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	response, state, err := s.collectCodeBuddyChatCompletions(resp, originalModel, upstreamModel, startTime)
	if err != nil {
		return nil, err
	}
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, response)
	return &OpenAIForwardResult{
		RequestID:       resp.Header.Get("x-request-id"),
		Usage:           state.Usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) collectCodeBuddyChatCompletions(
	resp *http.Response,
	originalModel string,
	upstreamModel string,
	startTime time.Time,
) (*apicompat.ChatCompletionsResponse, ccStreamScanState, error) {
	type bufferedChoice struct {
		index        int
		role         string
		content      strings.Builder
		reasoning    strings.Builder
		finishReason string
		toolCalls    map[int]*apicompat.ChatToolCall
	}

	choices := make(map[int]*bufferedChoice)
	var responseID string
	var responseModel string
	var created int64
	state := s.scanCCStream(resp, "codebuddy chat_completions", resp.Header.Get("x-request-id"), startTime, func(chunk *apicompat.ChatCompletionsChunk) {
		if responseID == "" {
			responseID = chunk.ID
		}
		if responseModel == "" {
			responseModel = chunk.Model
		}
		if created == 0 {
			created = chunk.Created
		}
		for _, deltaChoice := range chunk.Choices {
			choice := choices[deltaChoice.Index]
			if choice == nil {
				choice = &bufferedChoice{index: deltaChoice.Index, toolCalls: make(map[int]*apicompat.ChatToolCall)}
				choices[deltaChoice.Index] = choice
			}
			if deltaChoice.Delta.Role != "" {
				choice.role = deltaChoice.Delta.Role
			}
			if deltaChoice.Delta.Content != nil {
				choice.content.WriteString(*deltaChoice.Delta.Content)
			}
			if deltaChoice.Delta.ReasoningContent != nil {
				choice.reasoning.WriteString(*deltaChoice.Delta.ReasoningContent)
			} else if deltaChoice.Delta.Reasoning != nil {
				choice.reasoning.WriteString(*deltaChoice.Delta.Reasoning)
			}
			if deltaChoice.FinishReason != nil {
				choice.finishReason = *deltaChoice.FinishReason
			}
			for position, call := range deltaChoice.Delta.ToolCalls {
				callIndex := position
				if call.Index != nil {
					callIndex = *call.Index
				}
				merged := choice.toolCalls[callIndex]
				if merged == nil {
					copyCall := call
					copyCall.Index = nil
					merged = &copyCall
					choice.toolCalls[callIndex] = merged
					continue
				}
				if call.ID != "" {
					merged.ID = call.ID
				}
				if call.Type != "" {
					merged.Type = call.Type
				}
				if call.Function.Name != "" {
					merged.Function.Name = call.Function.Name
				}
				merged.Function.Arguments += call.Function.Arguments
			}
		}
	})
	if state.Err != nil {
		return nil, state, fmt.Errorf("read CodeBuddy SSE: %w", state.Err)
	}
	if !state.SawDone && len(choices) == 0 {
		return nil, state, fmt.Errorf("CodeBuddy SSE ended without output")
	}
	if responseID == "" {
		responseID = "chatcmpl-codebuddy"
	}
	if responseModel == "" {
		responseModel = upstreamModel
	}
	if created == 0 {
		created = time.Now().Unix()
	}

	indices := make([]int, 0, len(choices))
	for index := range choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	resultChoices := make([]apicompat.ChatChoice, 0, len(indices))
	for _, index := range indices {
		choice := choices[index]
		role := choice.role
		if role == "" {
			role = "assistant"
		}
		message := apicompat.ChatMessage{Role: role}
		if choice.content.Len() > 0 {
			content, _ := json.Marshal(choice.content.String())
			message.Content = content
		}
		if choice.reasoning.Len() > 0 {
			message.ReasoningContent = choice.reasoning.String()
		}
		callIndices := make([]int, 0, len(choice.toolCalls))
		for callIndex := range choice.toolCalls {
			callIndices = append(callIndices, callIndex)
		}
		sort.Ints(callIndices)
		for _, callIndex := range callIndices {
			message.ToolCalls = append(message.ToolCalls, *choice.toolCalls[callIndex])
		}
		resultChoices = append(resultChoices, apicompat.ChatChoice{
			Index:        choice.index,
			Message:      message,
			FinishReason: choice.finishReason,
		})
	}
	if len(resultChoices) == 0 {
		resultChoices = []apicompat.ChatChoice{{Index: 0, Message: apicompat.ChatMessage{Role: "assistant"}, FinishReason: "stop"}}
	}

	usage := chatUsageFromOpenAIUsage(state.Usage)
	response := &apicompat.ChatCompletionsResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   originalModel,
		Choices: resultChoices,
		Usage:   &usage,
	}
	return response, state, nil
}

func chatUsageFromOpenAIUsage(usage OpenAIUsage) apicompat.ChatUsage {
	return apicompat.ChatUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.InputTokens + usage.OutputTokens,
	}
}
