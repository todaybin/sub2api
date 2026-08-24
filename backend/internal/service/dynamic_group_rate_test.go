package service

import (
	"context"
	"testing"
	"time"
)

type dynamicGroupRateGroupRepoStub struct {
	GroupRepository
	group      *Group
	accountIDs []int64
	evaluation DynamicGroupRateEvaluation
	applied    bool
}

func (s *dynamicGroupRateGroupRepoStub) GetByIDLite(context.Context, int64) (*Group, error) {
	copy := *s.group
	return &copy, nil
}

func (s *dynamicGroupRateGroupRepoStub) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return s.accountIDs, nil
}

func (s *dynamicGroupRateGroupRepoStub) ApplyDynamicRateEvaluation(_ context.Context, _ int64, _ float64, evaluation DynamicGroupRateEvaluation) (*DynamicGroupRateApplyResult, error) {
	s.evaluation = evaluation
	s.applied = true
	return &DynamicGroupRateApplyResult{}, nil
}

type dynamicGroupRateAccountRepoStub struct {
	AccountRepository
	accounts []*Account
	enabled  bool
}

func (s *dynamicGroupRateAccountRepoStub) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return s.accounts, nil
}

func (s *dynamicGroupRateAccountRepoStub) EnableUpstreamBillingProbeForGroup(context.Context, int64) error {
	s.enabled = true
	return nil
}

type dynamicGroupRateSettingRepoStub struct {
	SettingRepository
	value string
}

func (s *dynamicGroupRateSettingRepoStub) GetValue(context.Context, string) (string, error) {
	return s.value, nil
}

func dynamicRateTestAccount(id int64, now time.Time, mode string, multiplier float64) *Account {
	freshUntil := now.Add(time.Hour)
	return &Account{
		ID: id, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{
			Status: UpstreamBillingProbeStatusOK,
			Data: map[string]any{
				"billing_scope":            "token",
				"billing_mode":             mode,
				"resolved_rate_multiplier": multiplier,
			},
			FreshUntil: &freshUntil,
		}},
	}
}

func newDynamicRateTestService(now time.Time, group *Group, accounts ...*Account) (*DynamicGroupRateService, *dynamicGroupRateGroupRepoStub, *dynamicGroupRateAccountRepoStub) {
	groupRepo := &dynamicGroupRateGroupRepoStub{group: group}
	accountRepo := &dynamicGroupRateAccountRepoStub{accounts: accounts}
	for _, account := range accounts {
		groupRepo.accountIDs = append(groupRepo.accountIDs, account.ID)
	}
	service := NewDynamicGroupRateService(groupRepo, accountRepo, nil, nil)
	service.now = func() time.Time { return now }
	return service, groupRepo, accountRepo
}

func TestDynamicGroupRateTargetRoundsToFourDecimals(t *testing.T) {
	if got := dynamicGroupRateTarget(0.8, 20); got != 0.96 {
		t.Fatalf("target = %v, want 0.96", got)
	}
	if got := dynamicGroupRateTarget(0.123456, 7.5); got != 0.1327 {
		t.Fatalf("target = %v, want 0.1327", got)
	}
}

func TestDynamicGroupRateDecision(t *testing.T) {
	tests := []struct {
		name      string
		current   float64
		target    float64
		complete  bool
		apply     bool
		direction string
	}{
		{name: "increase", current: 0.8, target: 0.96, complete: false, apply: true, direction: DynamicRateDirectionIncrease},
		{name: "decrease with complete data", current: 1.2, target: 0.96, complete: true, apply: true, direction: DynamicRateDirectionDecrease},
		{name: "decrease frozen with incomplete data", current: 1.2, target: 0.96, complete: false, apply: false, direction: DynamicRateDirectionNone},
		{name: "same rate", current: 0.96, target: 0.96, complete: true, apply: false, direction: DynamicRateDirectionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apply, direction := dynamicGroupRateDecision(tt.current, tt.target, tt.complete)
			if apply != tt.apply || direction != tt.direction {
				t.Fatalf("decision = (%v, %q), want (%v, %q)", apply, direction, tt.apply, tt.direction)
			}
		})
	}
}

func TestDynamicGroupRateReconcileUsesMaximumEligibleBalanceRate(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	group := &Group{ID: 7, Status: StatusActive, RateMode: GroupRateModeDynamic, RateMultiplier: 0.7, DynamicRateMarkupPercent: 20}
	maxBalance := dynamicRateTestAccount(1, now, "balance", 0.8)
	lowerBalance := dynamicRateTestAccount(2, now, "balance", 0.6)
	subscription := dynamicRateTestAccount(3, now, "subscription", 9)
	oauth := dynamicRateTestAccount(4, now, "balance", 8)
	oauth.Type = AccountTypeOAuth
	inactive := dynamicRateTestAccount(5, now, "balance", 7)
	inactive.Status = "inactive"

	service, groupRepo, accountRepo := newDynamicRateTestService(now, group, maxBalance, lowerBalance, subscription, oauth, inactive)
	if err := service.ReconcileGroup(context.Background(), group.ID); err != nil {
		t.Fatal(err)
	}
	if !accountRepo.enabled {
		t.Fatal("eligible account probes were not enabled")
	}
	if groupRepo.evaluation.Status != DynamicRateStatusReady {
		t.Fatalf("status = %q, want ready", groupRepo.evaluation.Status)
	}
	if groupRepo.evaluation.SourceMultiplier == nil || *groupRepo.evaluation.SourceMultiplier != 0.8 {
		t.Fatalf("source = %v, want 0.8", groupRepo.evaluation.SourceMultiplier)
	}
	if groupRepo.evaluation.TargetMultiplier == nil || *groupRepo.evaluation.TargetMultiplier != 0.96 {
		t.Fatalf("target = %v, want 0.96", groupRepo.evaluation.TargetMultiplier)
	}
}

func TestDynamicGroupRateReconcileFreezesIncompleteDecreaseButAllowsIncrease(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	valid := dynamicRateTestAccount(1, now, "balance", 0.8)
	stale := dynamicRateTestAccount(2, now, "balance", 0.6)
	past := now.Add(-time.Minute)
	stale.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot).FreshUntil = &past

	decreaseGroup := &Group{ID: 7, Status: StatusActive, RateMode: GroupRateModeDynamic, RateMultiplier: 1.2, DynamicRateMarkupPercent: 20}
	service, repo, _ := newDynamicRateTestService(now, decreaseGroup, valid, stale)
	if err := service.ReconcileGroup(context.Background(), decreaseGroup.ID); err != nil {
		t.Fatal(err)
	}
	if repo.evaluation.Status != DynamicRateStatusIncomplete || repo.evaluation.TargetMultiplier != nil {
		t.Fatalf("incomplete decrease should be frozen: %+v", repo.evaluation)
	}

	increaseGroup := &Group{ID: 8, Status: StatusActive, RateMode: GroupRateModeDynamic, RateMultiplier: 0.7, DynamicRateMarkupPercent: 20}
	service, repo, _ = newDynamicRateTestService(now, increaseGroup, valid, stale)
	if err := service.ReconcileGroup(context.Background(), increaseGroup.ID); err != nil {
		t.Fatal(err)
	}
	if repo.evaluation.Status != DynamicRateStatusIncomplete || repo.evaluation.TargetMultiplier == nil || *repo.evaluation.TargetMultiplier != 0.96 {
		t.Fatalf("incomplete increase should be applied: %+v", repo.evaluation)
	}
}

func TestDynamicGroupRateReconcilePausesWhenGlobalProbeIsDisabled(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	group := &Group{ID: 7, Status: StatusActive, RateMode: GroupRateModeDynamic, RateMultiplier: 1}
	service, groupRepo, accountRepo := newDynamicRateTestService(now, group)
	service.settingService = NewSettingService(&dynamicGroupRateSettingRepoStub{value: `{"enabled":false,"interval_minutes":60}`}, nil)

	if err := service.ReconcileGroup(context.Background(), group.ID); err != nil {
		t.Fatal(err)
	}
	if groupRepo.evaluation.Status != DynamicRateStatusPaused || groupRepo.evaluation.TargetMultiplier != nil {
		t.Fatalf("paused evaluation = %+v", groupRepo.evaluation)
	}
	if accountRepo.enabled {
		t.Fatal("account probes must not be enabled while global probing is disabled")
	}
}

func TestDynamicGroupRateReconcileCodeBuddyUsesMarkupOnly(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	group := &Group{
		ID:                       9,
		Platform:                 PlatformCodeBuddy,
		Status:                   StatusActive,
		RateMode:                 GroupRateModeDynamic,
		RateMultiplier:           0.8,
		DynamicRateMarkupPercent: 20,
	}
	service, repo, accountRepo := newDynamicRateTestService(now, group)
	service.settingService = NewSettingService(&dynamicGroupRateSettingRepoStub{value: `{"enabled":false}`}, nil)

	if err := service.ReconcileGroup(context.Background(), group.ID); err != nil {
		t.Fatal(err)
	}
	if accountRepo.enabled {
		t.Fatal("CodeBuddy groups must not enable the API-key billing probe")
	}
	if repo.evaluation.Status != DynamicRateStatusReady {
		t.Fatalf("status = %q, want ready", repo.evaluation.Status)
	}
	if repo.evaluation.SourceMultiplier == nil || *repo.evaluation.SourceMultiplier != 1 {
		t.Fatalf("source = %v, want 1", repo.evaluation.SourceMultiplier)
	}
	if repo.evaluation.TargetMultiplier == nil || *repo.evaluation.TargetMultiplier != 1.2 {
		t.Fatalf("target = %v, want 1.2", repo.evaluation.TargetMultiplier)
	}
}
