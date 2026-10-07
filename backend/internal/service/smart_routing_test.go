package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
	"time"
)

type smartCatalogRepo struct {
	AccountRepository
	accounts  []Account
	err       error
	platforms []string
}

func (r *smartCatalogRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]Account, error) {
	r.platforms = platforms
	return r.accounts, r.err
}
func TestSmartCatalogPersistentAliasesAndEmpty(t *testing.T) {
	future := time.Now().Add(time.Hour)
	repo := &smartCatalogRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future, Credentials: map[string]any{"model_mapping": map[string]any{"public-a": "gpt-5", "alias-b": "gpt-5", "wild-*": "gpt-5"}}}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"public-a": "gpt-5"}}}}}
	s := &GatewayService{accountRepo: repo}
	group := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	ids, err := s.SmartModelCatalog(context.Background(), group)
	require.NoError(t, err)
	require.Equal(t, []string{"alias-b", "public-a"}, ids)
	require.Equal(t, []string{PlatformOpenAI}, repo.platforms)
	repo.accounts = nil
	ids, err = s.SmartModelCatalog(context.Background(), group)
	require.NoError(t, err)
	require.Empty(t, ids)
	repo.err = errors.New("database failed")
	_, err = s.SmartModelCatalog(context.Background(), group)
	require.ErrorContains(t, err, "database failed")
}

func TestSmartCatalogChannelAliasesRequirePersistentAccount(t *testing.T) {
	group := &Group{ID: 9, Platform: PlatformOpenAI, Status: StatusActive}
	channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{9}, ModelMapping: map[string]map[string]string{PlatformOpenAI: {"public": "gpt-5", "missing": "unsupported", "wild-*": "gpt-5"}}}
	channels := &ChannelService{}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{9: PlatformOpenAI}))
	future := time.Now().Add(time.Hour)
	repo := &smartCatalogRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}}}}}
	s := &GatewayService{accountRepo: repo, channelService: channels}
	models, err := s.SmartModelCatalog(context.Background(), group)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5", "public"}, models)
	repo.accounts = nil
	models, err = s.SmartModelCatalog(context.Background(), group)
	require.NoError(t, err)
	require.Empty(t, models)
}

func TestSmartCompositeCatalogUsesPersistentAliasOwnership(t *testing.T) {
	future := time.Now().Add(time.Hour)
	repo := &smartCatalogRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future, Credentials: map[string]any{"model_mapping": map[string]any{"custom-alias": "gpt-5"}}}}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(func(context.Context, int64, string) (CompositeModelOwnership, error) {
		t.Fatal("discovery must not query temporary schedulability")
		return CompositeModelOwnership{}, nil
	})
	s := &GatewayService{accountRepo: repo, compositeResolver: resolver}
	models, err := s.SmartModelCatalog(context.Background(), &Group{ID: 9, Platform: PlatformComposite, Status: StatusActive})
	require.NoError(t, err)
	require.Equal(t, []string{"custom-alias"}, models)
}

func TestSmartSelectionTransfersReservationToBackground(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(fmt.Sprint("consume=", consume), func(t *testing.T) {
			releases := 0
			group := &Group{ID: 9}
			_, selection, err := finishSmartSelection(smartRouteCandidate{group: group, model: "gpt-5"}, &AccountSelectionResult{Account: &Account{ID: 7}, Acquired: true, ReleaseFunc: func() { releases++ }})
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), ctxkey.SmartSelection, selection)
			cleanup := TransferSmartSelection(ctx)
			selection.ReleaseRequest()
			require.Zero(t, releases, "HTTP completion must retain the background reservation")
			if consume {
				result := takeSmartSelection(ctx, &group.ID, "gpt-5", nil)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				result.ReleaseFunc()
			}
			cleanup()
			cleanup()
			selection.ReleaseRequest()
			require.Equal(t, 1, releases)
		})
	}
}

func TestSmartRandomChoosesGroupsWithoutAccountCountBias(t *testing.T) {
	var candidates []smartRouteCandidate
	for i := 0; i < 10; i++ {
		gid := int64(1)
		if i == 9 {
			gid = 2
		}
		candidates = append(candidates, smartRouteCandidate{group: &Group{ID: gid}, account: &Account{ID: int64(i + 1), Concurrency: 1}, load: &AccountLoadInfo{}})
	}
	counts := map[int64]int{}
	for i := 0; i < 4000; i++ {
		orderSmartCandidates(context.Background(), "random", candidates, nil)
		counts[candidates[0].group.ID]++
	}
	require.InDelta(t, 2000, counts[1], 200)
	require.InDelta(t, 2000, counts[2], 200)
}

func TestSmartSequentialPreservesCandidateGroupOrder(t *testing.T) {
	candidates := []smartRouteCandidate{
		{group: &Group{ID: 12}, account: &Account{ID: 1}},
		{group: &Group{ID: 12}, account: &Account{ID: 2}},
		{group: &Group{ID: 4}, account: &Account{ID: 3}},
		{group: &Group{ID: 9}, account: &Account{ID: 4}},
	}
	orderSmartCandidates(context.Background(), "sequential", candidates, nil)
	require.Equal(t, []int64{12, 12, 4, 9}, []int64{
		candidates[0].group.ID, candidates[1].group.ID, candidates[2].group.ID, candidates[3].group.ID,
	})
}
func TestSmartPriceAvailableFreeUnknownAndSpeed(t *testing.T) {
	candidates := []smartRouteCandidate{
		{group: &Group{ID: 1}, account: &Account{ID: 1, Concurrency: 1}, load: &AccountLoadInfo{}, price: math.Inf(1)},
		{group: &Group{ID: 2}, account: &Account{ID: 2, Concurrency: 1}, load: &AccountLoadInfo{}, price: 0},
		{group: &Group{ID: 3}, account: &Account{ID: 3, Concurrency: 1}, load: &AccountLoadInfo{CurrentConcurrency: 1}, price: 0},
		{group: &Group{ID: 4}, account: &Account{ID: 4, Concurrency: 1}, load: &AccountLoadInfo{}, price: 1},
	}
	orderSmartCandidates(context.Background(), "price", candidates, nil)
	require.Equal(t, int64(2), candidates[0].group.ID)
	require.Equal(t, int64(4), candidates[1].group.ID)
	require.Equal(t, int64(1), candidates[2].group.ID)
	require.Equal(t, int64(3), candidates[3].group.ID)
	for i := range candidates {
		candidates[i].knownTTFT = true
		candidates[i].ttft = float64(candidates[i].group.ID) * 10
	}
	orderSmartCandidates(context.Background(), "speed", candidates, nil)
	require.Equal(t, int64(1), candidates[0].group.ID)
	require.Equal(t, int64(3), candidates[3].group.ID)
}
func TestSmartSelectionReleaseAndRequestLocalGroup(t *testing.T) {
	original := &Account{ID: 7}
	releases := 0
	group := &Group{ID: 9}
	_, p, err := finishSmartSelection(smartRouteCandidate{group: group, model: "alias"}, &AccountSelectionResult{Account: original, ReleaseFunc: func() { releases++ }})
	require.NoError(t, err)
	require.Zero(t, original.RoutingGroupID)
	require.Equal(t, int64(9), p.Result.Account.RoutingGroupID)
	release := p.Result.ReleaseFunc
	ctx := context.WithValue(context.Background(), ctxkey.SmartSelection, p)
	require.Nil(t, takeSmartSelection(ctx, &group.ID, "different", nil))
	require.Equal(t, 1, releases)
	release()
	require.Equal(t, 1, releases)
	require.Nil(t, takeSmartSelection(ctx, &group.ID, "alias", nil))
}
func TestSmartReservationFinalCapabilityGate(t *testing.T) {
	group := &Group{ID: 9, Platform: PlatformOpenAI}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{9}, Credentials: map[string]any{openAIEndpointCapabilitiesCredentialKey: []any{"chat_completions"}}}
	releases := 0
	_, p, err := finishSmartSelection(smartRouteCandidate{group: group, model: "gpt-5"}, &AccountSelectionResult{Account: account, Acquired: true, ReleaseFunc: func() { releases++ }})
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ctxkey.SmartSelection, p)
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	_, _, err = s.SelectAccountWithSchedulerForCapability(ctx, &group.ID, "", "", "gpt-5", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityEmbeddings, false, false, true, PlatformOpenAI)
	require.Error(t, err)
	require.Equal(t, 1, releases)
}
func TestSmartContinuationDetection(t *testing.T) {
	require.True(t, smartHasContinuation([]byte(`{"input":[{"type":"function_call_output","call_id":"c"}]}`)))
	require.True(t, smartHasContinuation([]byte(`{"messages":[{"role":"tool","tool_call_id":"c"}]}`)))
	require.False(t, smartHasContinuation([]byte(`{"messages":[{"role":"user","content":"hello"}]}`)))
	require.False(t, smartHasContinuation([]byte(`{"messages":[{"role":"user","content":"Explain function_call_output, tool_result, tool_call_id and functionResponse"}]}`)))
	require.True(t, smartHasContinuation([]byte(`{"contents":[{"parts":[{"functionResponse":{"name":"search","response":{}}}]}]}`)))
}

type smartSelectionRepo struct {
	schedulerGroupAwareOpenAIAccountRepo
}

func (r smartSelectionRepo) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	var result []Account
	for _, account := range r.accounts {
		if openAIStickyAccountMatchesGroup(&account, &groupID) {
			result = append(result, account)
		}
	}
	return result, nil
}

type smartSelectionCache struct {
	*schedulerTestGatewayCache
	group int64
}

func (c *smartSelectionCache) GetSmartGroup(context.Context, int64, string) (int64, error) {
	return c.group, nil
}
func (c *smartSelectionCache) ClaimSmartGroup(_ context.Context, _ int64, _ string, expected, groupID int64, _ time.Duration) (int64, error) {
	if c.group == expected {
		c.group = groupID
	}
	return c.group, nil
}

func TestSmartRouteAdmissionAffinityAndExactGroup(t *testing.T) {
	groups := []*Group{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}}
	accounts := []Account{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{1}},
		{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{2}},
	}
	repo := smartSelectionRepo{schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
	cache := &smartSelectionCache{schedulerTestGatewayCache: &schedulerTestGatewayCache{}}
	var released []int64
	concurrency := NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &released})
	cfg := &config.Config{}
	gateway := &GatewayService{accountRepo: repo, cache: cache, concurrencyService: concurrency, cfg: cfg}
	openAI := &OpenAIGatewayService{accountRepo: repo, cache: cache, concurrencyService: concurrency, cfg: cfg}
	key := &APIKey{ID: 9, RoutingMode: "smart", RoutingStrategy: "random", SmartGroups: groups}
	req := SmartRouteRequest{Model: "gpt-5", Endpoint: CompositeRouteEndpointResponses, SessionHash: "conversation"}
	group, selection, err := gateway.SelectSmartRoute(context.Background(), key, req, openAI, func(g *Group) (bool, error) { return g.ID == 2, nil })
	require.NoError(t, err)
	require.Equal(t, int64(2), group.ID)
	require.Equal(t, int64(22), selection.Result.Account.ID)
	require.Equal(t, int64(2), selection.Result.Account.RoutingGroupID)
	require.Nil(t, key.GroupID)
	require.Zero(t, accounts[1].RoutingGroupID)
	releaseSmartSelection(selection.Result)
	req.Body = []byte(`{"input":[{"type":"function_call_output","call_id":"call","output":"ok"}]}`)
	group, selection, err = gateway.SelectSmartRoute(context.Background(), key, req, openAI, func(*Group) (bool, error) { return true, nil })
	require.NoError(t, err)
	require.True(t, selection.Pinned)
	require.Equal(t, int64(2), group.ID)
	require.Equal(t, int64(22), selection.Result.Account.ID)
	releaseSmartSelection(selection.Result)
	_, _, err = gateway.SelectSmartRoute(context.Background(), key, req, openAI, func(g *Group) (bool, error) { return g.ID == 1, nil })
	require.Error(t, err, "continuation must not move to another authorized group")
	_, _, err = gateway.SelectSmartRoute(context.Background(), key, req, openAI, func(*Group) (bool, error) { return false, errors.New("database offline") })
	require.ErrorContains(t, err, "database offline")
	require.Len(t, released, 2)
}
