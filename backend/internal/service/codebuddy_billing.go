package service

import (
	"math"
	"strconv"
	"strings"
)

// CodeBuddy's credit catalogue describes provider consumption, while the
// gateway charges in the same abstract unit as the existing token pricing.
// These constants intentionally do not represent a currency conversion.
const (
	defaultCodeBuddyReferenceCostUnits = 70.0
	defaultCodeBuddyReferenceCredits   = 2000.0
	defaultCodeBuddyTokensPerCredit    = 31874.0
	codeBuddyMinimumLocalMultiple      = 0.01

	CodeBuddyCredentialReferenceCostUnits = "reference_cost_units"
	CodeBuddyCredentialReferenceCredits   = "reference_credits"
	CodeBuddyCredentialTokensPerCredit    = "tokens_per_credit"
)

// CodeBuddyBillingSettings contains the currency-free upstream calibration.
// ReferenceCostUnits / ReferenceCredits / TokensPerCredit are persisted on each
// account independently so the provider's 70=2000 relationship can be changed
// without changing code or introducing a currency conversion.
type CodeBuddyBillingSettings struct {
	ReferenceCostUnits float64 `json:"reference_cost_units"`
	ReferenceCredits   float64 `json:"reference_credits"`
	TokensPerCredit    float64 `json:"tokens_per_credit"`
	BaseCostPerToken   float64 `json:"base_cost_per_token"`
}

func DefaultCodeBuddyBillingSettings() CodeBuddyBillingSettings {
	return CodeBuddyBillingSettings{
		ReferenceCostUnits: defaultCodeBuddyReferenceCostUnits,
		ReferenceCredits:   defaultCodeBuddyReferenceCredits,
		TokensPerCredit:    defaultCodeBuddyTokensPerCredit,
		BaseCostPerToken:   defaultCodeBuddyReferenceCostUnits / (defaultCodeBuddyReferenceCredits * defaultCodeBuddyTokensPerCredit),
	}
}

func DefaultCodeBuddyBaseCostPerToken() float64 {
	return DefaultCodeBuddyBillingSettings().BaseCostPerToken
}

func CodeBuddyBillingSettingsForAccount(account *Account) CodeBuddyBillingSettings {
	settings := DefaultCodeBuddyBillingSettings()
	if account == nil || account.Credentials == nil {
		return settings
	}
	parse := func(key string, fallback float64) float64 {
		raw, ok := account.Credentials[key]
	if !ok { return fallback }
	text, ok := codeBuddyStringValue(raw)
	if !ok { return fallback }
	value, parseErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if parseErr != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fallback
		}
		return value
	}
	settings.ReferenceCostUnits = parse(CodeBuddyCredentialReferenceCostUnits, settings.ReferenceCostUnits)
	settings.ReferenceCredits = parse(CodeBuddyCredentialReferenceCredits, settings.ReferenceCredits)
	settings.TokensPerCredit = parse(CodeBuddyCredentialTokensPerCredit, settings.TokensPerCredit)
	settings.BaseCostPerToken = settings.ReferenceCostUnits / (settings.ReferenceCredits * settings.TokensPerCredit)
	return settings
}

func validCodeBuddyPositiveNumber(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// CodeBuddyBillableTokens returns the normalized token count used by the
// upstream credit formula. Cache fields are separate in ClaudeUsage, so they
// are included exactly once here.
func CodeBuddyBillableTokens(usage ClaudeUsage) int {
	total := usage.InputTokens + usage.OutputTokens +
		usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	if total < 0 {
		return 0
	}
	return total
}

// CodeBuddyUpstreamCostPerToken converts a model's upstream credit multiplier
// into the local abstract cost unit. The value is deliberately currency-free.
func CodeBuddyUpstreamCostPerToken(modelMultiplier float64, configuredBase ...float64) float64 {
	if math.IsNaN(modelMultiplier) || math.IsInf(modelMultiplier, 0) || modelMultiplier <= 0 {
		return 0
	}
	base := DefaultCodeBuddyBaseCostPerToken()
	if len(configuredBase) > 0 && configuredBase[0] > 0 && !math.IsNaN(configuredBase[0]) && !math.IsInf(configuredBase[0], 0) {
		base = configuredBase[0]
	}
	return modelMultiplier * base
}

// CodeBuddyProviderCostMultiplier derives the request-level provider cost
// multiplier by comparing the upstream abstract cost with the system's base
// token cost. Group/user rates are intentionally excluded from baseCost.
func CodeBuddyProviderCostMultiplier(account *Account, baseCost float64, usage ClaudeUsage, models ...string) float64 {
	return codeBuddyProviderCostMultiplier(account, baseCost, usage, DefaultCodeBuddyBaseCostPerToken(), models...)
}

// CodeBuddyProviderCostMultiplierWithBase is the request billing entry point
// when the administrator supplied a custom abstract upstream baseline.
func CodeBuddyProviderCostMultiplierWithBase(account *Account, baseCost float64, usage ClaudeUsage, configuredBase float64, models ...string) float64 {
	return codeBuddyProviderCostMultiplier(account, baseCost, usage, configuredBase, models...)
}

func codeBuddyProviderCostMultiplier(account *Account, baseCost float64, usage ClaudeUsage, configuredBase float64, models ...string) float64 {
	if account == nil || account.Platform != PlatformCodeBuddy || baseCost <= 0 {
		return 1
	}
	tokens := CodeBuddyBillableTokens(usage)
	if tokens <= 0 {
		return 1
	}
	modelMultiplier := CodeBuddyModelCreditsMultiplier(account, models...)
	if modelMultiplier <= 0 {
		// An explicit zero from the catalogue is free upstream, but local
		// billing keeps the established minimum so a paid request cannot become
		// completely free because of malformed provider metadata.
		modelMultiplier = codeBuddyMinimumLocalMultiple
	}
	upstreamCost := float64(tokens) * CodeBuddyUpstreamCostPerToken(modelMultiplier, configuredBase)
	if upstreamCost <= 0 {
		return codeBuddyMinimumLocalMultiple
	}
	ratio := upstreamCost / baseCost
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return codeBuddyMinimumLocalMultiple
	}
	return ratio
}
