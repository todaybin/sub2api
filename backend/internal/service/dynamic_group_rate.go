package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

type DynamicGroupRateEvaluation struct {
	SourceMultiplier *float64
	Status           string
	EvaluatedAt      time.Time
	TargetMultiplier *float64
}

type DynamicGroupRateApplyResult struct {
	Applied       bool
	OldMultiplier float64
	NewMultiplier float64
	Direction     string
}

type DynamicGroupRateRepository interface {
	ApplyDynamicRateEvaluation(context.Context, int64, float64, DynamicGroupRateEvaluation) (*DynamicGroupRateApplyResult, error)
}

type DynamicGroupRateAccountRepository interface {
	EnableUpstreamBillingProbeForGroup(context.Context, int64) error
}

// dynamicGroupRateTarget applies the group markup and keeps billing multipliers
// at the same four-decimal precision as the persisted rate columns.
func dynamicGroupRateTarget(sourceMultiplier, markupPercent float64) float64 {
	return math.Round(sourceMultiplier*(1+markupPercent/100)*upstreamBillingProbeAccountRateScale) / upstreamBillingProbeAccountRateScale
}

func dynamicGroupRateDecision(currentMultiplier, targetMultiplier float64, complete bool) (bool, string) {
	if targetMultiplier <= 0 || math.IsNaN(targetMultiplier) || math.IsInf(targetMultiplier, 0) {
		return false, DynamicRateDirectionNone
	}
	if targetMultiplier > currentMultiplier {
		return true, DynamicRateDirectionIncrease
	}
	if targetMultiplier < currentMultiplier && complete {
		return true, DynamicRateDirectionDecrease
	}
	return false, DynamicRateDirectionNone
}

type DynamicGroupRateService struct {
	groupRepo            GroupRepository
	accountRepo          AccountRepository
	settingService       *SettingService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	now                  func() time.Time
}

func NewDynamicGroupRateService(
	groupRepo GroupRepository,
	accountRepo AccountRepository,
	settingService *SettingService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *DynamicGroupRateService {
	return &DynamicGroupRateService{
		groupRepo: groupRepo, accountRepo: accountRepo, settingService: settingService,
		authCacheInvalidator: authCacheInvalidator, now: time.Now,
	}
}

func (s *DynamicGroupRateService) ReconcileAll(ctx context.Context) error {
	if s == nil || s.groupRepo == nil {
		return nil
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].RateMode != GroupRateModeDynamic {
			continue
		}
		if err := s.ReconcileGroup(ctx, groups[i].ID); err != nil {
			slog.Warn("dynamic_group_rate_reconcile_failed", "group_id", groups[i].ID, "error", err)
		}
	}
	return nil
}

func (s *DynamicGroupRateService) ReconcileForAccount(ctx context.Context, account *Account) error {
	if s == nil || account == nil {
		return nil
	}
	seen := make(map[int64]struct{}, len(account.GroupIDs)+len(account.AccountGroups))
	for _, id := range account.GroupIDs {
		seen[id] = struct{}{}
	}
	for _, ag := range account.AccountGroups {
		seen[ag.GroupID] = struct{}{}
	}
	for id := range seen {
		if id <= 0 {
			continue
		}
		if err := s.ReconcileGroup(ctx, id); err != nil && err != ErrGroupNotFound {
			return err
		}
	}
	return nil
}

func (s *DynamicGroupRateService) ReconcileGroup(ctx context.Context, groupID int64) error {
	if s == nil || s.groupRepo == nil || s.accountRepo == nil || groupID <= 0 {
		return nil
	}
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return err
	}
	if group.RateMode != GroupRateModeDynamic || group.Status != StatusActive {
		return nil
	}
	now := s.now().UTC()
	settings := defaultUpstreamBillingProbeSettings()
	if s.settingService != nil {
		settings, err = s.settingService.GetUpstreamBillingProbeSettings(ctx)
		if err != nil {
			return err
		}
	}
	if !settings.Enabled {
		return s.apply(ctx, group, DynamicGroupRateEvaluation{Status: DynamicRateStatusPaused, EvaluatedAt: now})
	}
	if enabler, ok := s.accountRepo.(DynamicGroupRateAccountRepository); ok {
		if err := enabler.EnableUpstreamBillingProbeForGroup(ctx, groupID); err != nil {
			return err
		}
	}
	accountIDs, err := s.groupRepo.GetAccountIDsByGroupIDs(ctx, []int64{groupID})
	if err != nil {
		return err
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, accountIDs)
	if err != nil {
		return err
	}
	complete := true
	relevant := 0
	validBalance := 0
	maxRate := 0.0
	for _, account := range accounts {
		if account == nil || account.Type != AccountTypeAPIKey || account.Status != StatusActive || !account.Schedulable {
			continue
		}
		relevant++
		snapshot := decodeUpstreamBillingProbeSnapshot(account.Extra)
		if snapshot == nil || snapshot.Status != UpstreamBillingProbeStatusOK || snapshot.FreshUntil == nil || !snapshot.FreshUntil.After(now) {
			complete = false
			continue
		}
		if scope, _ := snapshot.Data["billing_scope"].(string); scope != "token" {
			complete = false
			continue
		}
		mode, _ := snapshot.Data["billing_mode"].(string)
		if mode == "subscription" {
			continue
		}
		if mode != "balance" {
			complete = false
			continue
		}
		rate, ok := resolveAccountExtraNumber(snapshot.Data, "resolved_rate_multiplier")
		if !ok || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > upstreamBillingRateSyncMaxMultiplier {
			complete = false
			continue
		}
		validBalance++
		if rate > maxRate {
			maxRate = rate
		}
	}
	status := DynamicRateStatusReady
	if relevant == 0 || validBalance == 0 {
		status = DynamicRateStatusWaiting
	} else if !complete {
		status = DynamicRateStatusIncomplete
	}
	evaluation := DynamicGroupRateEvaluation{Status: status, EvaluatedAt: now}
	if validBalance > 0 {
		source := math.Round(maxRate*upstreamBillingProbeAccountRateScale) / upstreamBillingProbeAccountRateScale
		target := dynamicGroupRateTarget(source, group.DynamicRateMarkupPercent)
		evaluation.SourceMultiplier = &source
		apply, _ := dynamicGroupRateDecision(group.RateMultiplier, target, complete)
		if apply {
			evaluation.TargetMultiplier = &target
		}
	}
	return s.apply(ctx, group, evaluation)
}

func (s *DynamicGroupRateService) apply(ctx context.Context, group *Group, evaluation DynamicGroupRateEvaluation) error {
	repo, ok := s.groupRepo.(DynamicGroupRateRepository)
	if !ok {
		return fmt.Errorf("dynamic group rate repository is unavailable")
	}
	result, err := repo.ApplyDynamicRateEvaluation(ctx, group.ID, group.DynamicRateMarkupPercent, evaluation)
	if err != nil {
		return err
	}
	if result != nil && result.Applied {
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, group.ID)
		}
		slog.Info("dynamic_group_rate_applied",
			"group_id", group.ID,
			"old_rate_multiplier", result.OldMultiplier,
			"new_rate_multiplier", result.NewMultiplier,
			"source_rate_multiplier", evaluation.SourceMultiplier,
			"markup_percent", group.DynamicRateMarkupPercent,
			"direction", result.Direction,
		)
	}
	return nil
}
