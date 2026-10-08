package service

import "context"

const (
	BalanceAutoRestoreTokenExtraKey  = "balance_auto_restore_token"
	SchedulingRevisionExtraKey       = "scheduling_revision"
	SchedulingManualPausedAtExtraKey = "scheduling_manual_paused_at"
)

// BalanceSchedulingRepository keeps automatic changes separate from admin overrides.
type BalanceSchedulingRepository interface {
	UpdateCodeBuddyBalanceScheduling(ctx context.Context, expected *Account, settings CodeBuddyCheckinSettings, disable, restore bool) error
}

type CodeBuddyBalanceSchedulingService interface {
	UpdateCodeBuddyBalanceScheduling(ctx context.Context, expected *Account, settings CodeBuddyCheckinSettings, disable, restore bool) error
}

// AccountSchedulingUpdater is for explicit scheduling settings in admin imports.
// Ordinary edits preserve the latest database scheduling state.
type AccountSchedulingUpdater interface {
	UpdateWithScheduling(ctx context.Context, account *Account, enabled bool) error
}

func (s *adminServiceImpl) UpdateCodeBuddyBalanceScheduling(ctx context.Context, expected *Account, settings CodeBuddyCheckinSettings, disable, restore bool) error {
	repo, ok := s.accountRepo.(BalanceSchedulingRepository)
	if !ok {
		return ErrUpstreamBillingProbeUnavailable
	}
	return repo.UpdateCodeBuddyBalanceScheduling(ctx, expected, settings, disable, restore)
}
