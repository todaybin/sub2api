package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/tidwall/gjson"
)

// SmartGroupAffinityCache deliberately does not widen GatewayCache (and its mocks).
// Claim is atomic across gateway instances. A concurrent request must use the winner.
type SmartGroupAffinityCache interface {
	GetSmartGroup(ctx context.Context, keyID int64, session string) (int64, error)
	ClaimSmartGroup(ctx context.Context, keyID int64, session string, expected, groupID int64, ttl time.Duration) (int64, error)
}

type SmartRouteRequest struct {
	Model, Endpoint, Platform, SessionHash string
	Path, ContentType                      string
	Body                                   []byte
	WebSocket                              bool
}

// SmartSelection is consumed once by the downstream scheduler. The request owner
// releases its reservation unless ownership is transferred to a background task.
type SmartSelection struct {
	mu          sync.Mutex
	release     func()
	transferred bool
	GroupID     int64
	Model       string
	Pinned      bool
	Result      *AccountSelectionResult
}

// ReleaseRequest releases reservations rejected or left unused by the request.
func (p *SmartSelection) ReleaseRequest() {
	if p == nil {
		return
	}
	p.mu.Lock()
	release := p.release
	if p.transferred {
		release = nil
	}
	p.mu.Unlock()
	if release != nil {
		release()
	}
}

// TransferSmartSelection keeps the reservation alive after an async HTTP response.
// The background task must defer the returned cleanup, even if it never schedules.
func TransferSmartSelection(ctx context.Context) func() {
	p, _ := ctx.Value(ctxkey.SmartSelection).(*SmartSelection)
	if p == nil {
		return func() {}
	}
	p.mu.Lock()
	p.transferred = true
	release := p.release
	p.mu.Unlock()
	if release == nil {
		return func() {}
	}
	return release
}

func smartSelectionPinned(ctx context.Context) bool {
	p, _ := ctx.Value(ctxkey.SmartSelection).(*SmartSelection)
	return p != nil && p.Pinned
}

func takeSmartSelection(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) *AccountSelectionResult {
	p, _ := ctx.Value(ctxkey.SmartSelection).(*SmartSelection)
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := p.Result
	p.Result = nil
	if result == nil || result.Account == nil {
		return nil
	}
	_, skip := excluded[result.Account.ID]
	if groupID == nil || *groupID != p.GroupID || model != p.Model || skip {
		releaseSmartSelection(result)
		return nil
	}
	return result
}
func releaseSmartSelection(selection *AccountSelectionResult) {
	if selection != nil && selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func SmartSessionHash(ctx context.Context) string {
	v, _ := ctx.Value(ctxkey.SmartSessionHash).(string)
	return v
}

// SmartModelCatalog uses persistent availability, never current concurrency or
// temporary cooldowns. Empty groups and repository failures cannot expose defaults.
func (s *GatewayService) SmartModelCatalog(ctx context.Context, group *Group) ([]string, error) {
	return s.SmartModelCatalogForEndpoint(ctx, group, CompositeRouteEndpointAny)
}

func (s *GatewayService) SmartModelCatalogForEndpoint(ctx context.Context, group *Group, endpoint string, targets ...string) ([]string, error) {
	if group == nil || !group.IsActive() {
		return []string{}, nil
	}
	platforms := []string{group.Platform}
	if group.Platform == PlatformGemini || group.Platform == PlatformAnthropic {
		platforms = append(platforms, PlatformAntigravity)
	}
	if group.Platform == PlatformComposite {
		platforms = []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformCodeBuddy, PlatformTypeSafe}
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, &group.ID, platforms, true)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" && !strings.ContainsAny(id, "*?") && (!group.ModelAllowlistEnabled() || group.ModelAllowlist.Allows(id)) {
			seen[id] = true
		}
	}
	for i := range accounts {
		a := &accounts[i]
		if !a.IsActive() || !a.Schedulable {
			continue
		}
		if group.Platform != PlatformComposite && a.Platform != group.Platform && !mixedListingAccountAllowed(group.Platform, a) {
			continue
		}
		mapping := a.GetModelMapping()
		if a.Platform == PlatformCodeBuddy {
			catalog := filterCodeBuddyCatalogForRegion(CodeBuddyStoredModelCatalog(a), CodeBuddyRegionForAccount(a))
			if len(catalog) > 0 {
				for _, m := range catalog {
					add(m.ID)
				}
			} else {
				for _, id := range CodeBuddyModelsForRegion(CodeBuddyRegionForAccount(a)) {
					add(id)
				}
			}
		}
		for id := range mapping {
			if a.Platform == group.Platform || group.Platform == PlatformComposite || mixedListingModelAllowed(group.Platform, id) {
				add(id)
			}
		}
		if len(mapping) == 0 || a.IsOpenAIPassthroughEnabled() {
			switch a.Platform {
			case PlatformOpenAI:
				for _, m := range openai.DefaultModels {
					add(m.ID)
				}
			case PlatformGemini:
				for _, m := range geminicli.DefaultModels {
					add(m.ID)
				}
			case PlatformAnthropic:
				for _, m := range claude.DefaultModels {
					add(m.ID)
				}
			case PlatformAntigravity:
				for _, m := range antigravity.DefaultModels() {
					if group.Platform == PlatformAntigravity || group.Platform == PlatformComposite || mixedListingModelAllowed(group.Platform, m.ID) {
						add(m.ID)
					}
				}
			case PlatformGrok:
				for _, id := range xai.DefaultModelIDs() {
					add(id)
				}
			}
		}
	}
	if len(accounts) > 0 && group.Platform == PlatformComposite && s.compositeResolver != nil && s.compositeResolver.repo != nil {
		routes, err := s.compositeResolver.repo.ListByGroup(ctx, group.ID, false)
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			if !route.Enabled || route.MatchType != "exact" {
				continue
			}
			model := route.UpstreamModel
			if model == "" {
				model = route.PublicModel
			}
			for i := range accounts {
				a := &accounts[i]
				if a.IsActive() && a.Schedulable && a.Platform == route.TargetPlatform && a.IsModelSupported(model) {
					add(route.PublicModel)
					break
				}
			}
		}
	}
	if group.Platform != PlatformComposite && s.channelService != nil {
		channel, err := s.channelService.GetChannelForGroup(ctx, group.ID)
		if err != nil {
			return nil, err
		}
		if channel != nil {
			for _, platform := range matchingPlatforms(group.Platform) {
				for public := range channel.ModelMapping[platform] {
					mapped := s.ResolveChannelMapping(ctx, group.ID, public).MappedModel
					for i := range accounts {
						a := &accounts[i]
						if a.IsActive() && a.Schedulable && (a.Platform == group.Platform || mixedListingAccountAllowed(group.Platform, a)) && a.IsModelSupported(mapped) {
							add(public)
							break
						}
					}
				}
			}
		}
	}
	// Use the same route rules with persistent ownership for discovery. The live
	// scheduler's ownership cache can omit temporarily throttled alias accounts.
	resolver := CompositeRouteResolver{}
	if s.compositeResolver != nil {
		resolver = *s.compositeResolver
	}
	resolver.modelOwnershipResolver = func(_ context.Context, _ int64, model string) (CompositeModelOwnership, error) {
		ownership := CompositeModelOwnership{}
		for _, a := range accounts {
			if !a.IsActive() || !a.Schedulable || !isConcreteRequestPlatform(a.Platform) || !explicitModelMappingClaims(a, model) {
				continue
			}
			if ownership.Matched && ownership.TargetPlatform != a.Platform {
				return CompositeModelOwnership{Ambiguous: true}, nil
			}
			ownership.Matched, ownership.TargetPlatform = true, a.Platform
		}
		return ownership, nil
	}
	models := make([]string, 0, len(seen))
	for id := range seen {
		if group.Platform == PlatformComposite {
			decision, err := resolver.Resolve(ctx, group.ID, id, endpoint)
			if err != nil {
				return nil, err
			}
			if !decision.Matched {
				continue
			}
			if len(targets) > 0 {
				allowed := false
				for _, platform := range targets {
					allowed = allowed || decision.TargetPlatform == platform
				}
				if !allowed {
					continue
				}
			}
			supported := false
			for i := range accounts {
				a := &accounts[i]
				if a.IsActive() && a.Schedulable && a.Platform == decision.TargetPlatform && a.IsModelSupported(decision.UpstreamModel) {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
		}
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}

type smartRouteCandidate struct {
	group           *Group
	account         *Account
	ctx             context.Context
	model, platform string
	load            *AccountLoadInfo
	price           float64
	ttft            float64
	knownTTFT       bool
}

var smartTTFT = struct {
	sync.Mutex
	values map[string]smartTTFTSample
}{values: make(map[string]smartTTFTSample)}

type smartTTFTSample struct {
	ms float64
	at time.Time
}

func observeSmartTTFT(accountID int64, model string, ms *int) {
	if ms == nil || *ms <= 0 || accountID <= 0 || model == "" {
		return
	}
	key := fmt.Sprintf("%d:%s", accountID, model)
	smartTTFT.Lock()
	defer smartTTFT.Unlock()
	now := time.Now()
	for k, v := range smartTTFT.values {
		if now.Sub(v.at) > time.Hour {
			delete(smartTTFT.values, k)
		}
	}
	prev := smartTTFT.values[key]
	value := float64(*ms)
	if now.Sub(prev.at) < time.Hour {
		value = prev.ms*.8 + value*.2
	}
	smartTTFT.values[key] = smartTTFTSample{value, now}
}

func smartLatency(accountID int64, model string) (float64, bool) {
	smartTTFT.Lock()
	defer smartTTFT.Unlock()
	v, ok := smartTTFT.values[fmt.Sprintf("%d:%s", accountID, model)]
	return v.ms, ok && time.Since(v.at) < time.Hour
}

func smartOpenAICapability(req SmartRouteRequest) OpenAIEndpointCapability {
	switch req.Endpoint {
	case CompositeRouteEndpointChatCompletions:
		return OpenAIEndpointCapabilityChatCompletions
	case CompositeRouteEndpointEmbeddings:
		return OpenAIEndpointCapabilityEmbeddings
	case "live":
		return OpenAIEndpointCapabilityLive
	case "alpha_search":
		return OpenAIEndpointCapabilityAlphaSearch
	case CompositeRouteEndpointResponses:
		return OpenAIEndpointCapabilityResponses
	default:
		return ""
	}
}

func smartPlatformSupports(platform string, req SmartRouteRequest) bool {
	if req.Platform != "" && platform != req.Platform {
		return false
	}
	if req.WebSocket {
		return platform == PlatformOpenAI || platform == PlatformGrok || IsMultiProtocolAPIKeyProvider(platform)
	}
	switch req.Endpoint {
	case "batch_image":
		return platform == PlatformGemini
	case CompositeRouteEndpointGemini:
		return platform == PlatformGemini || platform == PlatformAntigravity
	case CompositeRouteEndpointImages:
		return platform == PlatformOpenAI || platform == PlatformGrok
	case CompositeRouteEndpointEmbeddings, "live", "alpha_search":
		return platform == PlatformOpenAI
	case "video", "audio":
		return platform == PlatformGrok
	case "systemone":
		return platform == PlatformTypeSafe
	default:
		return platform != PlatformTypeSafe
	}
}

// estimateSmartPrice uses the same workload, mapping and downstream calculator
// for every candidate. Zero is a known free price; unknown ranks after known.
func (s *GatewayService) estimateSmartPrice(ctx context.Context, key *APIKey, req SmartRouteRequest, a *Account, upstream string, openAI *OpenAIGatewayService) float64 {
	if req.Endpoint == "batch_image" {
		var batch BatchImageSubmitRequest
		if json.Unmarshal(req.Body, &batch) != nil || len(batch.Items) == 0 {
			return math.Inf(1)
		}
		unit := key.Group.GetImagePrice(batch.ImageSize)
		if unit == nil || *unit < 0 {
			value, err := (&BatchImageModelPricingResolver{Resolver: s.resolver}).BatchImageUnitPrice(ctx, &BatchImageJob{Model: batch.Model})
			if err != nil {
				return math.Inf(1)
			}
			unit = &value
		}
		rate := s.ResolveUserGroupRateMultiplier(ctx, key.UserID, key.Group.ID, key.Group.RateMultiplier)
		if key.Group.ImageRateIndependent {
			rate = key.Group.ImageRateMultiplier
		}
		return *unit * math.Max(0, rate) * math.Max(0, a.BillingRateMultiplier()) * math.Max(0, key.Group.BatchImageDiscountMultiplier) * float64(len(batch.Items))
	}
	if s.billingService == nil && (openAI == nil || !a.IsOpenAICompatible() || openAI.billingService == nil) {
		return math.Inf(1)
	}
	rate, _ := s.inflightEstimateDeps().rates(ctx, key)
	model := req.Model
	mapping := ChannelMappingResult{}
	if openAI != nil && a.IsOpenAICompatible() {
		mapping = openAI.ResolveChannelMapping(ctx, key.Group.ID, model)
	} else if s.channelService != nil {
		mapping = s.channelService.ResolveChannelMapping(ctx, key.Group.ID, model)
	}
	{
		switch mapping.BillingModelSource {
		case BillingModelSourceUpstream:
			model = a.GetMappedModel(upstream)
		case BillingModelSourceChannelMapped:
			if mapping.MappedModel != "" {
				model = mapping.MappedModel
			}
		}
	}
	maxTokens := int(gjson.GetBytes(req.Body, "max_tokens").Int())
	if v := int(gjson.GetBytes(req.Body, "max_output_tokens").Int()); v > 0 {
		maxTokens = v
	}
	input, output := tokenCounts(inflightReservationCfg(s.cfg), len(req.Body), maxTokens)
	if openAI != nil && a.IsOpenAICompatible() {
		base := openAI.ResolveUserGroupRateMultiplier(ctx, key.UserID, key.Group.ID, key.Group.RateMultiplier)
		at := time.Now()
		text, image := computePeakAwareMultipliers(key, base, at)
		effort := gjson.GetBytes(req.Body, "reasoning.effort").String()
		if effort == "" {
			effort = gjson.GetBytes(req.Body, "reasoning_effort").String()
		}
		result := &OpenAIForwardResult{Model: req.Model, UpstreamModel: a.GetMappedModel(upstream), ReasoningEffort: &effort}
		media := ParseGrokMediaRequest(req.ContentType, req.Body)
		switch req.Endpoint {
		case CompositeRouteEndpointImages:
			result.ImageCount, result.ImageSize = media.N, media.SizeTier
		case "video":
			result.VideoCount = 1
			result.VideoResolution, result.VideoDurationSeconds = media.Resolution, media.DurationSeconds
		case "audio":
			mode := "tts"
			if strings.Contains(req.Path, "realtime") {
				mode = "realtime"
			}
			if strings.Contains(req.Path, "stt") || strings.Contains(req.Path, "transcriptions") {
				mode = "stt"
			}
			if strings.Contains(req.Path, "custom-voices") {
				return math.Inf(1) // No generation units can be estimated for voice management.
			}
			elapsed := time.Duration(0)
			if mode == "realtime" {
				elapsed = time.Minute // Compare all candidates using the same minute.
			}
			result.AudioUsage = estimateGrokVoiceAudioUsage(mode, req.Body, req.ContentType, nil, elapsed)
		case "alpha_search":
			result.WebSearchCalls = 1
		}
		tier := gjson.GetBytes(req.Body, "service_tier").String()
		if groupBillsOpenAIFastAtStandard(key, a, tier) {
			tier = ""
		}
		models := openAI.filterCNProviderBillingModelCandidates(ctx, a, key, usageBillingModelCandidates(model, mapping.MappedModel, req.Model, a.GetMappedModel(upstream), upstream))
		cost, err := openAI.calculateOpenAIRecordUsageCost(ctx, result, key, models, text, image, resolveVideoRateMultiplier(key, base), base, UsageTokens{InputTokens: input, OutputTokens: output}, tier, openAILongContextBillingGate(a), at)
		if err != nil || cost == nil || math.IsNaN(cost.ActualCost) || cost.ActualCost < 0 {
			return math.Inf(1)
		}
		return cost.ActualCost
	}
	if rate == 0 {
		return 0
	}
	model = s.billableModelWithFallback(ctx, key, model, a.GetMappedModel(upstream), upstream)
	effort := gjson.GetBytes(req.Body, "reasoning.effort").String()
	if effort == "" {
		effort = gjson.GetBytes(req.Body, "reasoning_effort").String()
	}

	cost, err := s.billingService.CalculateTokenCostForRequest(TokenCostRequest{
		Ctx: ctx, Model: model, Group: key.Group, Tokens: UsageTokens{InputTokens: input, OutputTokens: output}, RateMultiplier: rate,
		PricingAt: time.Now(), ServiceTier: gjson.GetBytes(req.Body, "service_tier").String(),
		ReasoningEffort: effort, Resolver: s.resolver,
	})
	if err != nil || cost == nil || math.IsNaN(cost.ActualCost) || cost.ActualCost < 0 {
		return math.Inf(1)
	}
	return cost.ActualCost
}

// SelectSmartRoute runs admission before scheduling and returns a concrete group
// and an existing scheduler reservation. No protocol/billing policy sees a stand-in group.
func (s *GatewayService) SelectSmartRoute(ctx context.Context, key *APIKey, req SmartRouteRequest, openAI *OpenAIGatewayService, admit func(*Group) (bool, error)) (*Group, *SmartSelection, error) {
	var candidates []smartRouteCandidate
	var loads []AccountWithConcurrency
	allGroupAccounts := map[int64][]Account{}
	for _, group := range key.SmartGroups {
		if req.Endpoint == "batch_image" && (group.Platform != PlatformGemini || !group.AllowBatchImageGeneration || group.IsSubscriptionType()) {
			continue
		}
		if !group.IsActive() || (group.ClaudeCodeOnly && !IsClaudeCodeClient(ctx)) || (group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(req.Model)) {
			continue
		}
		allowed, err := admit(group)
		if err != nil {
			return nil, nil, err
		}
		if !allowed {
			continue
		}
		platform, model := group.Platform, req.Model
		groupCtx := s.withGroupContext(context.WithValue(ctx, ctxkey.SelectedGroupID, group.ID), group)
		if platform == PlatformComposite {
			decision, ok, err := s.resolveCompositeRouteDecision(groupCtx, group, model, req.Endpoint)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
			platform, model = decision.TargetPlatform, decision.UpstreamModel
			groupCtx = WithCompositeRouteDecision(groupCtx, decision)
		}
		if group.Platform != PlatformComposite && req.Endpoint != "batch_image" {
			mapping := s.ResolveChannelMapping(groupCtx, group.ID, req.Model)
			if mapping.Mapped && mapping.MappedModel != "" {
				model = mapping.MappedModel
			}
		}
		if !smartPlatformSupports(platform, req) {
			continue
		}
		accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, group.ID)
		if err != nil {
			return nil, nil, err
		}
		if platform != PlatformOpenAI && !IsMultiProtocolAPIKeyProvider(platform) && platform != PlatformGrok {
			groupCtx = s.withGatewayProfitControlGate(groupCtx, &group.ID)
		}
		if openAI != nil {
			groupCtx = openAI.withOpenAIQuotaAutoPauseContext(groupCtx)
			if req.Endpoint == CompositeRouteEndpointImages || req.Endpoint == "video" || req.Endpoint == "audio" {
				groupCtx = WithOpenAIProfitControlSuppressed(groupCtx)
			} else {
				groupCtx = openAI.withOpenAIProfitControlGate(groupCtx, &group.ID)
			}
		}
		allGroupAccounts[group.ID] = accounts
		if s.checkChannelPricingRestriction(groupCtx, &group.ID, model) {
			continue
		}
		for i := range accounts {
			a := &accounts[i]
			if req.Endpoint == "batch_image" {
				registry := NewDefaultBatchImageProviderRegistry()
				supported := false
				for _, name := range batchImageProviderSelectionOrder(gjson.GetBytes(req.Body, "provider").String()) {
					provider, ok := registry.Get(name)
					if ok && provider.SupportsAccount(a) {
						supported = true
					}
				}
				if !supported {
					continue
				}
			}
			if a.Platform != platform && !mixedListingAccountAllowed(platform, a) {
				continue
			}
			if !s.isAccountSchedulableForSelection(a) || !s.isModelSupportedByAccountWithContext(groupCtx, a, model) || !s.isAccountSchedulableForModelSelection(groupCtx, a, model) {
				continue
			}
			if !a.IsOpenAICompatible() && (!s.isAccountSchedulableForQuota(a) || !s.isAccountSchedulableForWindowCost(groupCtx, a, req.SessionHash != "") || !s.isAccountSchedulableForRPM(groupCtx, a, req.SessionHash != "") || !s.isGatewayAccountProfitEligible(groupCtx, a)) {
				continue
			}
			if openAI != nil && a.IsOpenAICompatible() {
				checker := &defaultOpenAIAccountScheduler{service: openAI}
				capability, imageCapability := smartOpenAICapability(req), OpenAIImagesCapability("")
				if req.Endpoint == CompositeRouteEndpointImages {
					if a.IsGrok() {
						capability = OpenAIEndpointCapabilityGrokMediaGeneration
					} else {
						imageCapability = OpenAIImagesCapabilityBasic
					}
				}
				if req.Endpoint == "video" {
					capability = OpenAIEndpointCapabilityGrokMediaGeneration
				}
				if !checker.isAccountRequestCompatible(groupCtx, a, OpenAIAccountScheduleRequest{GroupID: &group.ID, Platform: platform, RequestedModel: model, RequiredCapability: capability, RequiredImageCapability: imageCapability, RequirePrivacySet: group.RequirePrivacySet || openAI.openAIGroupRequiresPrivacySet(groupCtx, &group.ID)}) {
					continue
				}
			}
			copyKey := *key
			copyKey.Group, copyKey.GroupID = group, &group.ID
			candidate := smartRouteCandidate{group: group, account: a, ctx: groupCtx, model: model, platform: platform, price: math.Inf(1)}
			if key.RoutingStrategy == "price" {
				candidate.price = s.estimateSmartPrice(groupCtx, &copyKey, req, a, model, openAI)
			}
			latencyModel := a.GetMappedModel(model)
			if a.IsOpenAICompatible() {
				latencyModel = canonicalOpenAIAccountSchedulingModel(a, model)
			}
			candidate.ttft, candidate.knownTTFT = smartLatency(a.ID, latencyModel)
			candidates = append(candidates, candidate)
			loads = append(loads, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.EffectiveLoadFactor()})
		}
	}
	if len(candidates) == 0 {
		return nil, nil, ErrNoAvailableAccounts
	}
	loadMap := map[int64]*AccountLoadInfo{}
	if s.concurrencyService != nil {
		var err error
		loadMap, err = s.concurrencyService.GetAccountsLoadBatch(ctx, loads)
		if err != nil {
			return nil, nil, err
		}
	}
	for i := range candidates {
		candidates[i].load = loadMap[candidates[i].account.ID]
		if candidates[i].load == nil {
			candidates[i].load = &AccountLoadInfo{AccountID: candidates[i].account.ID}
		}
	}
	orderSmartCandidates(ctx, key.RoutingStrategy, candidates, openAI)
	// previous_response_id belongs to a group-scoped upstream session. Discover
	// its owner before choosing; unresolved continuation fails closed.
	previous := strings.TrimSpace(gjson.GetBytes(req.Body, "previous_response_id").String())
	owner, ownerAccount := int64(0), int64(0)
	if previous != "" && openAI != nil {
		for _, c := range candidates {
			if id := openAI.ResolveAccountIDByPreviousResponseIDForScheduler(c.ctx, &c.group.ID, previous, c.model, nil, smartOpenAICapability(req), false); id > 0 {
				owner, ownerAccount = c.group.ID, id
				break
			}
		}
		if owner == 0 {
			return nil, nil, fmt.Errorf("smart routing cannot resolve previous_response_id owner")
		}
	}
	cache, hasCache := s.cache.(SmartGroupAffinityCache)
	expected := int64(0)
	if req.SessionHash != "" {
		if !hasCache {
			return nil, nil, errors.New("smart group affinity cache unavailable")
		}
		bound, err := cache.GetSmartGroup(ctx, key.ID, req.SessionHash)
		if err != nil {
			return nil, nil, err
		}
		if owner != 0 && bound != 0 && owner != bound {
			return nil, nil, errors.New("smart session and previous response group disagree")
		}
		expected = bound
		if bound != 0 {
			owner = bound
		}
	}
	pinned := previous != "" || smartHasContinuation(req.Body)
	if pinned && owner == 0 {
		return nil, nil, errors.New("smart continuation requires an existing session owner")
	}
	if pinned && ownerAccount == 0 {
		var err error
		for _, c := range candidates {
			if c.group.ID != owner {
				continue
			}
			if openAI != nil && c.account.IsOpenAICompatible() {
				ownerAccount, err = openAI.getStickySessionAccountID(ctx, &owner, req.SessionHash)
			} else {
				ownerAccount, err = s.cache.GetSessionAccountID(ctx, owner, req.SessionHash)
			}
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("resolve smart continuation account: %w", err)
		}
		if ownerAccount == 0 {
			return nil, nil, errors.New("smart continuation account is unavailable")
		}
	}
	// A healthy bound group is scheduled normally, preserving existing account
	// stickiness and its escape rules. Fresh choices constrain the ranked account
	// so downstream selection cannot silently overturn the strategy.
	for round := 0; round < 3; round++ {
		ordered := append([]smartRouteCandidate(nil), candidates...)
		if owner != 0 {
			sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].group.ID == owner && ordered[j].group.ID != owner })
		}
		attemptedGroup := map[int64]bool{}
		var waiting *AccountSelectionResult
		var waitingCandidate smartRouteCandidate
		releaseWaiting := func() {
			if waiting != nil && waiting.ReleaseFunc != nil {
				waiting.ReleaseFunc()
			}
			waiting = nil
		}
		conflict := false
		for _, candidate := range ordered {
			c := candidate
			bound := owner != 0 && c.group.ID == owner
			if pinned && owner != 0 && !bound {
				continue
			}
			if pinned && c.account.ID != ownerAccount {
				continue
			}
			if attemptedGroup[c.group.ID] {
				continue
			}
			exclusions := map[int64]struct{}{}
			// Existing sessions and random groups use the complete existing scheduler.
			if !pinned && (bound || key.RoutingStrategy == "random" || (key.RoutingStrategy == "auto" && len(c.group.GetRoutingAccountIDs(c.model)) > 0)) {
				attemptedGroup[c.group.ID] = true
			} else {
				for _, a := range allGroupAccounts[c.group.ID] {
					if a.ID != c.account.ID {
						exclusions[a.ID] = struct{}{}
					}
				}
			}
			var selection *AccountSelectionResult
			var err error
			if openAI != nil && c.account.IsOpenAICompatible() {
				transport := OpenAIUpstreamTransportHTTPSSE
				if req.WebSocket {
					transport = OpenAIUpstreamTransportResponsesWebsocketV2Ingress
				}
				capability, imageCapability := smartOpenAICapability(req), OpenAIImagesCapability("")
				if req.Endpoint == CompositeRouteEndpointImages {
					if c.account.IsGrok() {
						capability = OpenAIEndpointCapabilityGrokMediaGeneration
					} else {
						imageCapability = OpenAIImagesCapabilityBasic
					}
				}
				if req.Endpoint == "video" {
					capability = OpenAIEndpointCapabilityGrokMediaGeneration
				}
				selection, _, err = openAI.selectAccountWithScheduler(c.ctx, &c.group.ID, previous, req.SessionHash, c.model, exclusions, transport, capability, imageCapability, false, c.platform, false, true)
			} else {
				selection, err = s.SelectAccountWithLoadAwareness(c.ctx, &c.group.ID, req.SessionHash, c.model, exclusions, "", key.UserID)
			}
			if err != nil {
				if !errors.Is(err, ErrNoAvailableAccounts) {
					releaseWaiting()
					return nil, nil, err
				}
				if bound && pinned {
					releaseWaiting()
					return nil, nil, err
				}
				continue
			}
			if selection == nil || selection.Account == nil {
				continue
			}
			if pinned && selection.Account.ID != ownerAccount {
				releaseSmartSelection(selection)
				releaseWaiting()
				return nil, nil, errors.New("smart continuation cannot change upstream account")
			}
			if !selection.Acquired && !bound {
				if waiting == nil {
					waiting, waitingCandidate = selection, c
				} else if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				continue
			}
			releaseWaiting()
			if req.SessionHash != "" {
				winner, err := cache.ClaimSmartGroup(ctx, key.ID, req.SessionHash, expected, c.group.ID, stickySessionTTL)
				if err != nil {
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					return nil, nil, err
				}
				if winner != c.group.ID {
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					expected, owner = winner, winner
					conflict = true
					break
				}
			}
			group, reservation, err := finishSmartSelection(c, selection)
			reservation.Pinned = pinned
			return group, reservation, err
		}
		if conflict {
			continue
		}
		if waiting != nil {
			if req.SessionHash != "" {
				winner, err := cache.ClaimSmartGroup(ctx, key.ID, req.SessionHash, expected, waitingCandidate.group.ID, stickySessionTTL)
				if err != nil {
					releaseWaiting()
					return nil, nil, err
				}
				if winner != waitingCandidate.group.ID {
					releaseWaiting()
					expected, owner = winner, winner
					continue
				}
			}
			group, reservation, err := finishSmartSelection(waitingCandidate, waiting)
			reservation.Pinned = pinned
			return group, reservation, err
		}
		return nil, nil, ErrNoAvailableAccounts
	}
	return nil, nil, errors.New("smart session group changed concurrently; retry request")
}

func finishSmartSelection(c smartRouteCandidate, selection *AccountSelectionResult) (*Group, *SmartSelection, error) {
	accountCopy := *selection.Account
	accountCopy.RoutingGroupID = c.group.ID
	selection.Account = &accountCopy
	if selection.ReleaseFunc != nil {
		release := selection.ReleaseFunc
		var once sync.Once
		selection.ReleaseFunc = func() { once.Do(release) }
	}
	return c.group, &SmartSelection{GroupID: c.group.ID, Model: c.model, Result: selection, release: selection.ReleaseFunc}, nil
}

func smartHasContinuation(body []byte) bool {
	var continuation func(gjson.Result) bool
	continuation = func(value gjson.Result) bool {
		if value.IsObject() {
			kind := value.Get("type").String()
			if kind == "function_call_output" || kind == "tool_result" || value.Get("role").String() == "tool" || value.Get("tool_call_id").Exists() || value.Get("functionResponse").Exists() {
				return true
			}
		}
		found := false
		if value.IsArray() || value.IsObject() {
			value.ForEach(func(_, child gjson.Result) bool {
				found = continuation(child)
				return !found
			})
		}
		return found
	}
	for _, path := range []string{"input", "messages", "contents"} {
		if continuation(gjson.GetBytes(body, path)) {
			return true
		}
	}
	return false
}

func smartImmediatelyAvailable(c smartRouteCandidate) bool {
	return c.account.Concurrency <= 0 || c.load.CurrentConcurrency < c.account.Concurrency
}

func orderSmartCandidates(ctx context.Context, strategy string, candidates []smartRouteCandidate, openAI *OpenAIGatewayService) {
	// Shuffle ties rather than allowing configured group order to become policy.
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if strategy == "random" {
		groups := map[int64]int{}
		for _, c := range candidates {
			if _, ok := groups[c.group.ID]; !ok {
				groups[c.group.ID] = rand.Int()
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if smartImmediatelyAvailable(candidates[i]) != smartImmediatelyAvailable(candidates[j]) {
				return smartImmediatelyAvailable(candidates[i])
			}
			return groups[candidates[i].group.ID] < groups[candidates[j].group.ID]
		})
		return
	}
	// Reuse the configured modern scheduler's score and randomness across all
	// OpenAI-compatible candidates; the group-local scheduler retains final gates.
	advancedOrder := map[int64]int{}
	if openAI != nil && openAI.isOpenAIAdvancedSchedulerEnabled(ctx) {
		if scheduler, ok := openAI.getOpenAIAccountScheduler(ctx).(*defaultOpenAIAccountScheduler); ok {
			var accounts []*Account
			loads := map[int64]*AccountLoadInfo{}
			for _, c := range candidates {
				if c.account.IsOpenAICompatible() && loads[c.account.ID] == nil {
					accounts = append(accounts, c.account)
					loads[c.account.ID] = c.load
				}
			}
			plan := scheduler.buildOpenAIAccountLoadPlan(ctx, OpenAIAccountScheduleRequest{UseUpstreamTokenCost: true, SubscriptionPriority: openAI.isOpenAIAdvancedSchedulerSubscriptionPriorityEnabled(ctx)}, accounts, loads)
			for i, c := range plan.selectionOrder {
				advancedOrder[c.account.ID] = i
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if smartImmediatelyAvailable(a) != smartImmediatelyAvailable(b) {
			return smartImmediatelyAvailable(a)
		}
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		if a.load.LoadRate != b.load.LoadRate {
			return a.load.LoadRate < b.load.LoadRate
		}
		if a.account.LastUsedAt == nil {
			return b.account.LastUsedAt != nil
		}
		return b.account.LastUsedAt != nil && a.account.LastUsedAt.Before(*b.account.LastUsedAt)
	})
	// Apply the modern scheduler within its own candidates after a total baseline
	// order. Mixing two different comparators in one sort is not transitive.
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && smartImmediatelyAvailable(candidates[start]) == smartImmediatelyAvailable(candidates[end]) {
			end++
		}
		var ranked []smartRouteCandidate
		for _, c := range candidates[start:end] {
			if _, ok := advancedOrder[c.account.ID]; ok {
				ranked = append(ranked, c)
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return advancedOrder[ranked[i].account.ID] < advancedOrder[ranked[j].account.ID] })
		next := 0
		for i := start; i < end; i++ {
			if _, ok := advancedOrder[candidates[i].account.ID]; ok {
				candidates[i] = ranked[next]
				next++
			}
		}
		start = end
	}
	if strategy == "price" {
		sort.SliceStable(candidates, func(i, j int) bool {
			if smartImmediatelyAvailable(candidates[i]) != smartImmediatelyAvailable(candidates[j]) {
				return smartImmediatelyAvailable(candidates[i])
			}
			return candidates[i].price < candidates[j].price
		})
	}
	if strategy == "speed" {
		for start := 0; start < len(candidates); {
			end := start + 1
			for end < len(candidates) && smartImmediatelyAvailable(candidates[start]) == smartImmediatelyAvailable(candidates[end]) {
				end++
			}
			var sampled []smartRouteCandidate
			for _, c := range candidates[start:end] {
				if c.knownTTFT {
					sampled = append(sampled, c)
				}
			}
			sort.SliceStable(sampled, func(i, j int) bool { return sampled[i].ttft < sampled[j].ttft })
			next := 0
			for i := start; i < end; i++ {
				if candidates[i].knownTTFT {
					candidates[i] = sampled[next]
					next++
				}
			}
			start = end
		}
	}

}
