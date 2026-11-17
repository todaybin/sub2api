package service

import (
	"context"
	"math"
	"regexp"
	"time"
)

// ModelNumericParameter 描述已确认的参数支持状态和数值范围；缺省表示未知。
type ModelNumericParameter struct {
	Supported bool     `json:"supported"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
}

// ModelReasoningCapabilities 保留提供商的原始档位标识。
type ModelReasoningCapabilities struct {
	Supported     *bool                  `json:"supported,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	Efforts       []string               `json:"efforts,omitempty"`
	DefaultEffort string                 `json:"defaultEffort,omitempty"`
	BudgetTokens  *ModelNumericParameter `json:"budgetTokens,omitempty"`
}

// ModelParameterCapabilities 只包含公开参数，不包含账号、路由和凭据。
type ModelParameterCapabilities struct {
	Supported         *bool                       `json:"supported,omitempty"`
	Reasoning         *ModelReasoningCapabilities `json:"reasoning,omitempty"`
	ContextWindow     int64                       `json:"contextWindow,omitempty"`
	MaxContextWindow  int64                       `json:"maxContextWindow,omitempty"`
	MaxOutputTokens   int64                       `json:"maxOutputTokens,omitempty"`
	InputModalities   []string                    `json:"inputModalities,omitempty"`
	Tools             *bool                       `json:"tools,omitempty"`
	ParallelToolCalls *bool                       `json:"parallelToolCalls,omitempty"`
	StructuredOutput  *bool                       `json:"structuredOutput,omitempty"`
	Temperature       *ModelNumericParameter      `json:"temperature,omitempty"`
	TopP              *ModelNumericParameter      `json:"topP,omitempty"`
}

// ModelCapabilities 是跨 Sub2API、Gateway 和宿主 SDK 的增量能力契约。
type ModelCapabilities struct {
	ModelParameterCapabilities
	Version    int                                   `json:"version"`
	Source     string                                `json:"source"`
	ObservedAt string                                `json:"observedAt,omitempty"`
	Endpoints  map[string]ModelParameterCapabilities `json:"endpoints,omitempty"`
}

var integrationEffortPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

func rawModelEfforts(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if len(out) == 32 {
			break
		}
		if integrationEffortPattern.MatchString(value) && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}
func capabilityBool(value bool) *bool { return &value }
func metadataParameters(m UpstreamModelMetadata) ModelParameterCapabilities {
	out := ModelParameterCapabilities{ContextWindow: m.ContextWindow, MaxContextWindow: m.MaxContextWindow, MaxOutputTokens: m.MaxOutputTokens, InputModalities: normalizeCodexInputModalities(m.InputModalities), Tools: m.Tools, ParallelToolCalls: m.ParallelToolCalls, StructuredOutput: m.StructuredOutput, Temperature: m.Temperature, TopP: m.TopP}
	for _, n := range []*int64{&out.ContextWindow, &out.MaxContextWindow, &out.MaxOutputTokens} {
		if *n < 0 || *n > 1000000000 {
			*n = 0
		}
	}
	out.Temperature = boundedCapabilityRange(out.Temperature)
	out.TopP = boundedCapabilityRange(out.TopP)
	if m.Reasoning != nil || len(m.SupportedReasoningLevels) > 0 {
		out.Reasoning = &ModelReasoningCapabilities{Supported: m.Reasoning, Efforts: rawModelEfforts(m.SupportedReasoningLevels)}
		if len(out.Reasoning.Efforts) > 0 {
			out.Reasoning.Mode = "effort"
			if stringSliceContains(out.Reasoning.Efforts, m.DefaultReasoningLevel) {
				out.Reasoning.DefaultEffort = m.DefaultReasoningLevel
			}
		}
	}
	return out
}
func intersectCapabilityBool(values []*bool) *bool {
	unknown := false
	for _, value := range values {
		if value == nil {
			unknown = true
		} else if !*value {
			return capabilityBool(false)
		}
	}
	if unknown || len(values) == 0 {
		return nil
	}
	return capabilityBool(true)
}
func intersectCapabilityLimit(values []int64) int64 {
	var out int64
	for _, value := range values {
		if value <= 0 {
			return 0
		}
		if out == 0 || value < out {
			out = value
		}
	}
	return out
}
func intersectCapabilityStrings(values [][]string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values[0]...)
	for _, value := range values[1:] {
		out = intersectOrderedStrings(out, value)
	}
	return out
}
func intersectCapabilityRange(values []*ModelNumericParameter) *ModelNumericParameter {
	var out *ModelNumericParameter
	for _, value := range values {
		if value != nil && !value.Supported {
			return &ModelNumericParameter{Supported: false}
		}
	}
	for _, value := range values {
		if value == nil {
			return nil
		}
		if out == nil {
			copy := *value
			out = &copy
			continue
		}
		if out.Min == nil || value.Min == nil {
			out.Min = nil
		} else if *value.Min > *out.Min {
			out.Min = value.Min
		}
		if out.Max == nil || value.Max == nil {
			out.Max = nil
		} else if *value.Max < *out.Max {
			out.Max = value.Max
		}
	}
	if out != nil && out.Min != nil && out.Max != nil && *out.Min > *out.Max {
		return &ModelNumericParameter{Supported: false}
	}
	return out
}

// IntersectModelParameters 只声明所有可能执行路由共同支持的参数。
func IntersectModelParameters(values []ModelParameterCapabilities) ModelParameterCapabilities {
	var out ModelParameterCapabilities
	if len(values) == 0 {
		return out
	}
	bools := func(get func(ModelParameterCapabilities) *bool) *bool {
		v := make([]*bool, 0, len(values))
		for _, x := range values {
			v = append(v, get(x))
		}
		return intersectCapabilityBool(v)
	}
	limits := func(get func(ModelParameterCapabilities) int64) int64 {
		v := make([]int64, 0, len(values))
		for _, x := range values {
			v = append(v, get(x))
		}
		return intersectCapabilityLimit(v)
	}
	out.Supported = bools(func(x ModelParameterCapabilities) *bool { return x.Supported })
	out.Tools = bools(func(x ModelParameterCapabilities) *bool { return x.Tools })
	out.ParallelToolCalls = bools(func(x ModelParameterCapabilities) *bool { return x.ParallelToolCalls })
	out.StructuredOutput = bools(func(x ModelParameterCapabilities) *bool { return x.StructuredOutput })
	out.ContextWindow = limits(func(x ModelParameterCapabilities) int64 { return x.ContextWindow })
	out.MaxContextWindow = limits(func(x ModelParameterCapabilities) int64 { return x.MaxContextWindow })
	out.MaxOutputTokens = limits(func(x ModelParameterCapabilities) int64 { return x.MaxOutputTokens })
	modalities := [][]string{}
	efforts := [][]string{}
	reasoning := []*bool{}
	temperatures := []*ModelNumericParameter{}
	tops := []*ModelNumericParameter{}
	budgets := []*ModelNumericParameter{}
	mode, def := "", ""
	allReasoning := true
	for i, x := range values {
		modalities = append(modalities, x.InputModalities)
		temperatures = append(temperatures, x.Temperature)
		tops = append(tops, x.TopP)
		if x.Reasoning == nil {
			allReasoning = false
			reasoning = append(reasoning, nil)
			efforts = append(efforts, nil)
			budgets = append(budgets, nil)
			continue
		}
		reasoning = append(reasoning, x.Reasoning.Supported)
		efforts = append(efforts, x.Reasoning.Efforts)
		budgets = append(budgets, x.Reasoning.BudgetTokens)
		if i == 0 {
			mode = x.Reasoning.Mode
			def = x.Reasoning.DefaultEffort
		} else {
			if mode != x.Reasoning.Mode {
				mode = ""
			}
			if def != x.Reasoning.DefaultEffort {
				def = ""
			}
		}
	}
	out.InputModalities = intersectCapabilityStrings(modalities)
	out.Temperature = intersectCapabilityRange(temperatures)
	out.TopP = intersectCapabilityRange(tops)
	support := intersectCapabilityBool(reasoning)
	levels := intersectCapabilityStrings(efforts)
	if support != nil || len(levels) > 0 {
		out.Reasoning = &ModelReasoningCapabilities{Supported: support}
		if allReasoning {
			out.Reasoning.Mode = mode
			out.Reasoning.Efforts = levels
			out.Reasoning.BudgetTokens = intersectCapabilityRange(budgets)
			if stringSliceContains(levels, def) {
				out.Reasoning.DefaultEffort = def
			}
		}
	}
	return out
}

// MergeModelCapabilities 合并同一 Key 可达的分组，端点分别求交集，未知来源不会提升能力。
func MergeModelCapabilities(values []ModelCapabilities) ModelCapabilities {
	out := ModelCapabilities{Version: 1, Source: "upstream-snapshot", Endpoints: map[string]ModelParameterCapabilities{}}
	common := []ModelParameterCapabilities{}
	for _, endpoint := range []string{"chat_completions", "responses", "messages"} {
		params := []ModelParameterCapabilities{}
		for _, v := range values {
			if v.Endpoints == nil {
				params = append(params, ModelParameterCapabilities{})
				continue
			}
			if p, ok := v.Endpoints[endpoint]; ok && (p.Supported == nil || *p.Supported) {
				params = append(params, p)
			}
		}
		if len(params) > 0 {
			p := IntersectModelParameters(params)
			out.Endpoints[endpoint] = p
			common = append(common, p)
		} else {
			out.Endpoints[endpoint] = ModelParameterCapabilities{Supported: capabilityBool(false)}
		}
	}
	out.ModelParameterCapabilities = IntersectModelParameters(common)
	observedKnown := len(values) > 0
	for _, v := range values {
		if v.ObservedAt == "" {
			observedKnown = false
		}
		if out.ObservedAt == "" || v.ObservedAt < out.ObservedAt {
			out.ObservedAt = v.ObservedAt
		}
	}
	if !observedKnown {
		out.ObservedAt = ""
	}
	return out
}

// SmartModelCapabilities 每个分组一次读取候选账号，不按模型逐条查询数据库。
func (s *GatewayService) SmartModelCapabilities(ctx context.Context, group *Group, models []string) (map[string]ModelCapabilities, error) {
	result := map[string]ModelCapabilities{}
	if group == nil || !group.IsActive() {
		return result, nil
	}
	platforms := []string{group.Platform}
	if group.Platform == PlatformGemini || group.Platform == PlatformAnthropic {
		platforms = append(platforms, PlatformAntigravity)
	}
	if group.Platform == PlatformComposite {
		platforms = []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformCodeBuddy, PlatformTypeSafe}
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, &group.ID, platforms, false)
	if err != nil {
		return nil, err
	}
	var routes []CompositeModelRoute
	if group.Platform == PlatformComposite && s.compositeResolver != nil && s.compositeResolver.repo != nil {
		routes, err = s.compositeResolver.repo.ListByGroup(ctx, group.ID, false)
		if err != nil {
			return nil, err
		}
	}
	for _, model := range models {
		cap := ModelCapabilities{Version: 1, Source: "upstream-snapshot", Endpoints: map[string]ModelParameterCapabilities{}}
		observedKnown := true
		for _, endpoint := range []string{CompositeRouteEndpointChatCompletions, CompositeRouteEndpointResponses, CompositeRouteEndpointMessages} {
			platform, target := group.Platform, model
			params := []ModelParameterCapabilities{}
			groupCtx := s.withGroupContext(ctx, group)
			if platform == PlatformComposite {
				var ok bool
				platform, target, ok = integrationCompositeTarget(routes, accounts, model, endpoint)
				if !ok {
					cap.Endpoints[endpoint] = ModelParameterCapabilities{}
					continue
				}
			} else if mapping := s.ResolveChannelMapping(groupCtx, group.ID, model); mapping.Mapped && mapping.MappedModel != "" {
				target = mapping.MappedModel
			}
			if !smartPlatformSupports(platform, SmartRouteRequest{Endpoint: endpoint}) {
				cap.Endpoints[endpoint] = ModelParameterCapabilities{Supported: capabilityBool(false)}
				continue
			}
			for i := range accounts {
				account := &accounts[i]
				if !account.IsActive() || !account.Schedulable || !codexAccountServesPlatformModel(account, platform, target) || !account.IsModelSupported(target) {
					continue
				}
				if ids := group.GetRoutingAccountIDs(model); len(ids) > 0 && !containsInt64(ids, account.ID) {
					continue
				}
				if account.IsOpenAICompatible() && endpoint != CompositeRouteEndpointMessages && !account.SupportsOpenAIEndpointCapability(smartOpenAICapability(SmartRouteRequest{Endpoint: endpoint})) {
					continue
				}
				metadata, ok := account.GetUpstreamModelMetadata(account.GetMappedModel(target))
				p := ModelParameterCapabilities{}
				snapshot := account.GetUpstreamModelMetadataSnapshot()
				if ok && snapshot != nil {
					p = metadataParameters(metadata)
					if _, e := time.Parse(time.RFC3339, snapshot.SyncedAt); e != nil {
						observedKnown = false
					} else if cap.ObservedAt == "" || snapshot.SyncedAt < cap.ObservedAt {
						cap.ObservedAt = snapshot.SyncedAt
					}
				} else {
					observedKnown = false
				}
				p.Supported = capabilityBool(true)
				params = append(params, p)
			}
			if len(params) == 0 {
				cap.Endpoints[endpoint] = ModelParameterCapabilities{Supported: capabilityBool(false)}
			} else {
				cap.Endpoints[endpoint] = IntersectModelParameters(params)
			}
		}
		if !observedKnown {
			cap.ObservedAt = ""
		}
		common := []ModelParameterCapabilities{}
		for _, endpoint := range []string{"chat_completions", "responses", "messages"} {
			p := cap.Endpoints[endpoint]
			if p.Supported == nil || *p.Supported {
				common = append(common, p)
			}
		}
		cap.ModelParameterCapabilities = IntersectModelParameters(common)
		result[model] = cap
	}
	return result, nil
}

// 路由规则和模型映射批量读取，模型能力查询不触发逐模型数据库读取。
func integrationCompositeTarget(routes []CompositeModelRoute, accounts []Account, model, endpoint string) (string, string, bool) {
	if route, ok := matchCompositeRoute(routes, model, endpoint); ok {
		target := route.UpstreamModel
		if target == "" {
			target = model
		}
		return route.TargetPlatform, target, true
	}
	platforms := map[string]bool{}
	for _, account := range accounts {
		if explicitModelMappingClaims(account, model) {
			platforms[account.Platform] = true
		}
	}
	if len(platforms) > 1 {
		return "", "", false
	}
	for platform := range platforms {
		return platform, model, true
	}
	platform, ok := DetectModelPlatform(model)
	return platform, model, ok
}

// 异常快照参数保留为未知，避免污染整个账号目录。
func boundedCapabilityRange(r *ModelNumericParameter) *ModelNumericParameter {
	if r == nil {
		return nil
	}
	for _, n := range []*float64{r.Min, r.Max} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || math.Abs(*n) > 1000000000) {
			return nil
		}
	}
	if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
		return nil
	}
	return r
}
