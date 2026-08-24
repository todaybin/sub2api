package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const codeBuddyCheckinLeaderKey = "codebuddy:checkin:leader"

// CodeBuddyCheckinRunnerService executes each account's independent check-in
// schedule. State is persisted in accounts.extra, so no migration is needed.
type CodeBuddyCheckinRunnerService struct {
	admin     AdminService
	oauth     *CodeBuddyOAuthService
	cfg       *config.Config
	lock      LeaderLockCache
	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
	owner     string
}

func NewCodeBuddyCheckinRunnerService(admin AdminService, oauth *CodeBuddyOAuthService, cfg *config.Config) *CodeBuddyCheckinRunnerService {
	return &CodeBuddyCheckinRunnerService{admin: admin, oauth: oauth, cfg: cfg, owner: fmt.Sprintf("codebuddy-checkin-%d", time.Now().UnixNano())}
}

func (s *CodeBuddyCheckinRunnerService) SetLeaderLock(lock LeaderLockCache) { s.lock = lock }

func (s *CodeBuddyCheckinRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}
		c := cron.New(cron.WithParser(cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow)), cron.WithLocation(loc))
		if _, err := c.AddFunc("* * * * *", s.runScheduled); err != nil {
			logger.LegacyPrintf("service.codebuddy_checkin", "runner not started: %v", err)
			return
		}
		s.cron = c
		c.Start()
		logger.LegacyPrintf("service.codebuddy_checkin", "runner started (tick=every minute)")
	})
}

func (s *CodeBuddyCheckinRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			<-s.cron.Stop().Done()
		}
	})
}

func (s *CodeBuddyCheckinRunnerService) runScheduled() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lock, nil, codeBuddyCheckinLeaderKey, s.owner, 9*time.Minute)
	if !ok {
		return
	}
	defer release()
	accounts, err := s.admin.ListAccountsForSchedulerScoreFilter(ctx, PlatformCodeBuddy, AccountTypeOAuth, "", "", 0, "")
	if err != nil {
		logger.LegacyPrintf("service.codebuddy_checkin", "list accounts failed: %v", err)
		return
	}
	now := time.Now()
	for i := range accounts {
		account := accounts[i]
		settings := CodeBuddyCheckinSettingsForAccount(&account)
		if !settings.Enabled {
			continue
		}
		if settings.NextRunAt != "" {
			if due, err := time.Parse(time.RFC3339, settings.NextRunAt); err == nil && due.After(now) {
				continue
			}
		}
		if err := s.runOne(ctx, &account, settings); err != nil {
			logger.LegacyPrintf("service.codebuddy_checkin", "account=%d check-in failed: %v", account.ID, err)
		}
	}
}

func (s *CodeBuddyCheckinRunnerService) runOne(ctx context.Context, account *Account, settings CodeBuddyCheckinSettings) error {
	result, updated, err := s.oauth.Checkin(ctx, account)
	settings.LastRunAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		settings.LastStatus, settings.LastAction, settings.LastError = "failed", "checkin", err.Error()
		if strings.Contains(err.Error(), "12153") {
			settings.AuthInvalid = true
		}
	} else {
		settings.LastStatus, settings.LastAction, settings.LastMessage = result.Status, result.Action, result.Message
		settings.LastError, settings.AuthInvalid = "", result.AuthInvalid
		settings.TrialClaimed = settings.TrialClaimed || result.TrialClaimed
		settings.LastCreditGranted = result.CreditGranted
		// A successful upstream grant is sufficient evidence to recover an
		// account that this service previously disabled, even if the follow-up
		// resource query is temporarily unavailable.
		if result.CreditGranted > 0 && settings.AutoDisabledByBalance {
			if _, recoverErr := s.admin.SetAccountSchedulable(ctx, account.ID, true); recoverErr == nil {
				settings.AutoDisabledByBalance = false
			}
		}
		if updated != nil {
			if remaining, hasData, creditErr := s.oauth.CreditRemaining(ctx, updated); creditErr == nil && hasData {
				settings.LastCreditRemaining = remaining
				if remaining <= 0 {
					// Only mark accounts that were schedulable before this probe. A
					// manually disabled account must never be auto-enabled later.
					if account.Schedulable && !settings.AutoDisabledByBalance {
						settings.AutoDisabledByBalance = true
						_, _ = s.admin.SetAccountSchedulable(ctx, account.ID, false)
					}
				} else if settings.AutoDisabledByBalance {
					// A successful check-in can replenish credits. Restore only the
					// scheduler state that this service disabled itself.
					if _, recoverErr := s.admin.SetAccountSchedulable(ctx, account.ID, true); recoverErr == nil {
						settings.AutoDisabledByBalance = false
					}
				}
			}
		}
	}
	next, cronErr := checkinNextRun(settings.Cron, time.Now())
	if cronErr != nil {
		settings.LastStatus, settings.LastError = "failed", cronErr.Error()
		settings.NextRunAt = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	} else {
		settings.NextRunAt = next.UTC().Format(time.RFC3339)
	}
	if persistErr := s.admin.UpdateAccountExtra(ctx, account.ID, map[string]any{CodeBuddyCheckinExtraKey: settings}); persistErr != nil {
		logger.LegacyPrintf("service.codebuddy_checkin", "account=%d persist failed: %v", account.ID, persistErr)
		if err == nil {
			return persistErr
		}
	}
	return err
}

func checkinNextRun(expr string, from time.Time) (time.Time, error) {
	if strings.TrimSpace(expr) == "" {
		expr = CodeBuddyCheckinDefaultCron
	}
	sched, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

func (s *CodeBuddyCheckinRunnerService) RunNow(ctx context.Context, accountID int64) (*Account, error) {
	account, err := s.admin.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Platform != PlatformCodeBuddy || account.Type != AccountTypeOAuth {
		return nil, fmt.Errorf("codebuddy oauth account required")
	}
	settings := CodeBuddyCheckinSettingsForAccount(account)
	if err := s.runOne(ctx, account, settings); err != nil {
		return nil, err
	}
	return s.admin.GetAccount(ctx, accountID)
}
