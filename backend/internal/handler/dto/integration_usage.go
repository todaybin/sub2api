package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Integration responses use an explicit allowlist. Do not embed either the
// admin DTO or the user DTO, which both contain upstream fields.
type IntegrationUsageLog struct {
	ID                  int64     `json:"id"`
	UserID              int64     `json:"user_id"`
	APIKeyID            int64     `json:"api_key_id"`
	RequestID           string    `json:"request_id"`
	Model               string    `json:"model"`
	GroupID             *int64    `json:"group_id"`
	SubscriptionID      *int64    `json:"subscription_id"`
	InputTokens         int       `json:"input_tokens"`
	OutputTokens        int       `json:"output_tokens"`
	CacheCreationTokens int       `json:"cache_creation_tokens"`
	CacheReadTokens     int       `json:"cache_read_tokens"`
	InputCost           float64   `json:"input_cost"`
	OutputCost          float64   `json:"output_cost"`
	CacheCreationCost   float64   `json:"cache_creation_cost"`
	CacheReadCost       float64   `json:"cache_read_cost"`
	TotalCost           float64   `json:"total_cost"`
	ActualCost          float64   `json:"actual_cost"`
	RateMultiplier      float64   `json:"rate_multiplier"`
	BillingType         int8      `json:"billing_type"`
	PointsCost          float64   `json:"points_cost"`
	PointsPerUSD        float64   `json:"points_per_usd"`
	RequestType         string    `json:"request_type"`
	Stream              bool      `json:"stream"`
	OpenAIWSMode        bool      `json:"openai_ws_mode"`
	DurationMs          *int      `json:"duration_ms"`
	FirstTokenMs        *int      `json:"first_token_ms"`
	CreatedAt           time.Time `json:"created_at"`
}

func IntegrationUsageLogFromService(log *service.UsageLog) *IntegrationUsageLog {
	if log == nil {
		return nil
	}
	// The ordinary mapper selects the client-requested model and normalizes the
	// request type; copy only the public billing fields from its result.
	base := UsageLogFromService(log)
	return &IntegrationUsageLog{
		ID: base.ID, UserID: base.UserID, APIKeyID: base.APIKeyID,
		RequestID: base.RequestID, Model: base.Model, GroupID: base.GroupID, SubscriptionID: base.SubscriptionID,
		InputTokens: base.InputTokens, OutputTokens: base.OutputTokens,
		CacheCreationTokens: base.CacheCreationTokens, CacheReadTokens: base.CacheReadTokens,
		InputCost: base.InputCost, OutputCost: base.OutputCost,
		CacheCreationCost: base.CacheCreationCost, CacheReadCost: base.CacheReadCost,
		TotalCost: base.TotalCost, ActualCost: base.ActualCost, RateMultiplier: base.RateMultiplier,
		BillingType: base.BillingType, PointsCost: base.PointsCost, PointsPerUSD: base.PointsPerUSD,
		RequestType: base.RequestType, Stream: base.Stream, OpenAIWSMode: base.OpenAIWSMode,
		DurationMs: base.DurationMs, FirstTokenMs: base.FirstTokenMs, CreatedAt: base.CreatedAt,
	}
}

type IntegrationUsageStats struct {
	TotalRequests            int64   `json:"total_requests"`
	TotalInputTokens         int64   `json:"total_input_tokens"`
	TotalOutputTokens        int64   `json:"total_output_tokens"`
	TotalCacheTokens         int64   `json:"total_cache_tokens"`
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`
	TotalTokens              int64   `json:"total_tokens"`
	TotalCost                float64 `json:"total_cost"`
	TotalActualCost          float64 `json:"total_actual_cost"`
	TotalPointsCost          float64 `json:"total_points_cost"`
	AverageDurationMs        float64 `json:"average_duration_ms"`
}

func IntegrationUsageStatsFromStats(stats *usagestats.UsageStats) *IntegrationUsageStats {
	if stats == nil {
		return &IntegrationUsageStats{}
	}
	return &IntegrationUsageStats{
		TotalRequests: stats.TotalRequests, TotalInputTokens: stats.TotalInputTokens,
		TotalOutputTokens: stats.TotalOutputTokens, TotalCacheTokens: stats.TotalCacheTokens,
		TotalCacheCreationTokens: stats.TotalCacheCreationTokens, TotalCacheReadTokens: stats.TotalCacheReadTokens,
		TotalTokens: stats.TotalTokens, TotalCost: stats.TotalCost, TotalActualCost: stats.TotalActualCost,
		TotalPointsCost: stats.TotalPointsCost, AverageDurationMs: stats.AverageDurationMs,
	}
}
