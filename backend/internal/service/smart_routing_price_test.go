//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSmartPriceUsesGroupPricingAndNativeMediaRates(t *testing.T) {
	ctx := context.Background()
	bs := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, bs)
	s := &GatewayService{billingService: bs, resolver: resolver, cfg: &config.Config{}}
	openAI := &OpenAIGatewayService{billingService: bs, resolver: resolver, cfg: &config.Config{}}
	group := &Group{ID: 100, Platform: PlatformOpenAI, RateMultiplier: 2, ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"public"}, BillingMode: BillingModeToken, InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(5e-6)}}}
	key := &APIKey{UserID: 1, User: &User{ID: 1}, Group: group, GroupID: &group.ID}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req := SmartRouteRequest{Model: "public", Body: []byte(`{"model":"public","max_output_tokens":100}`)}
	input, output := tokenCounts(inflightReservationCfg(s.cfg), len(req.Body), 100)
	want := (float64(input)*1e-6 + float64(output)*5e-6) * 2
	require.InDelta(t, want, s.estimateSmartPrice(ctx, key, req, account, "unpriced-upstream", openAI), 1e-12)
	group.RateMultiplier = 0
	require.Zero(t, s.estimateSmartPrice(ctx, key, req, account, "unpriced-upstream", openAI))
	group.ModelPricing = nil
	group.RateMultiplier = 1
	require.True(t, math.IsInf(s.estimateSmartPrice(ctx, key, req, account, "unpriced-upstream", openAI), 1))
	group.RateMultiplier, group.ImageRateIndependent, group.ImageRateMultiplier = 0, true, 3
	group.ImagePrice1K = testPtrFloat64(0.25)
	req = SmartRouteRequest{Model: "gpt-image-1", Endpoint: CompositeRouteEndpointImages, ContentType: "application/json", Body: []byte(`{"model":"gpt-image-1","n":2,"size":"1024x1024"}`)}
	require.InDelta(t, 1.5, s.estimateSmartPrice(ctx, key, req, account, req.Model, openAI), 1e-12, "free text must not suppress independent image charges")
}

func TestSmartBatchPriceMatchesSettlementSnapshot(t *testing.T) {
	ctx := context.Background()
	group := &Group{ID: 100, Platform: PlatformGemini, RateMultiplier: 10, ImageRateIndependent: true, ImageRateMultiplier: 2, ImagePrice2K: testPtrFloat64(0.3), BatchImageDiscountMultiplier: 0.5}
	key := &APIKey{UserID: 1, Group: group, GroupID: &group.ID}
	account := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey, RateMultiplier: testPtrFloat64(1.5)}
	req := SmartRouteRequest{Model: "gemini-image", Endpoint: "batch_image", Body: []byte(`{"model":"gemini-image","image_size":"2K","items":[{"prompt":"a"},{"prompt":"b"}]}`)}
	s := &GatewayService{}
	// Two items, group image rate 2, account rate 1.5, batch discount 0.5.
	require.InDelta(t, 0.9, s.estimateSmartPrice(ctx, key, req, account, req.Model, nil), 1e-12)
	group.ImageRateMultiplier = 0
	require.Zero(t, s.estimateSmartPrice(ctx, key, req, account, req.Model, nil))
}
