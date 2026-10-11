package service

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const SettingKeyPointsPerUSD = "points_per_usd"
const RedeemTypePoints = "points"

// PointsRepository is deliberately separate from UserRepository so money-only
// integrations do not acquire new, unrelated capabilities.
type PointsRepository interface {
	PointsAccount(context.Context, int64) (*PointsAccountInfo, error)
	PointsSummary(context.Context, int64) (*PointsSummary, error)
	PointsAvailable(context.Context, int64) (float64, error)
	ReservePoints(context.Context, string, int64, int64, int64, float64) (float64, error)
	RenewPoints(context.Context, string) error
	ReleasePoints(context.Context, string) error
	RecoverPoints(context.Context) error
	AdjustPoints(context.Context, int64, float64, string, string, string) (BalanceChange, error)
	ListPointsLedger(context.Context, int64, int, int) ([]PointsLedgerEntry, int64, error)
}

// Recovery effects use the same platform-quota policy as normal settlement.
// The repository calls the observer only after a newly applied debit commits.
type pointsRecoveryWithEffects interface {
	RecoverPointsWithEffects(context.Context, func(*UsageBillingCommand)) error
}

type PointsAccountInfo struct {
	Balance  float64 `json:"points_balance"`
	Reserved float64 `json:"points_reserved"`
}

type PointsSummary struct {
	PointsSpent   float64 `json:"points_spent"`
	PointsCostUSD float64 `json:"points_cost_usd"`
	MoneySpentUSD float64 `json:"money_spent_usd"`
}

func (s *BillingCacheService) GetPointsAccount(ctx context.Context, userID int64) (*PointsAccountInfo, error) {
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return nil, ErrBillingServiceUnavailable
	}
	return repo.PointsAccount(ctx, userID)
}

type PointsLedgerEntry struct {
	ID           int64     `json:"id"`
	Amount       float64   `json:"amount"`
	BalanceAfter float64   `json:"balance_after"`
	CostUSD      float64   `json:"cost_usd"`
	PointsPerUSD float64   `json:"points_per_usd"`
	Kind         string    `json:"kind"`
	Notes        string    `json:"notes"`
	RequestID    *string   `json:"request_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func PointsCost(cost, rate float64) float64 {
	if cost <= 0 || rate <= 0 || math.IsNaN(cost) || math.IsNaN(rate) || math.IsInf(cost, 0) || math.IsInf(rate, 0) {
		return 0
	}
	value, _ := decimal.NewFromFloat(QuantizeUsageBillingAmount(cost)).Mul(decimal.NewFromFloat(rate)).Round(8).Float64()
	return value
}

type pointsPaymentContextKey struct{}

// PointsPayment holds the admission-time exchange rate and references from the
// handler and billing tasks. Pending settlements are never refunded on release.
type PointsPayment struct {
	ID                        string
	Rate                      float64
	UserID, APIKeyID, GroupID int64
	repo                      PointsRepository
	refs                      atomic.Int64
	stop                      chan struct{}
	once                      sync.Once
	handlerOnce               sync.Once
}

func PointsPaymentFromContext(ctx context.Context) *PointsPayment {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(pointsPaymentContextKey{}).(*PointsPayment)
	return p
}
func CopyPointsPayment(parent, target context.Context) context.Context {
	if p := PointsPaymentFromContext(parent); p != nil {
		return context.WithValue(target, pointsPaymentContextKey{}, p)
	}
	return target
}

func ClearPaymentReservations(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, pointsPaymentContextKey{}, (*PointsPayment)(nil))
	return context.WithValue(ctx, inflightReservationCtxKey{}, (*InflightReservation)(nil))
}
func (p *PointsPayment) Acquire() func() {
	if p == nil {
		return func() {}
	}
	p.refs.Add(1)
	var once sync.Once
	return func() { once.Do(p.done) }
}
func (p *PointsPayment) done() {
	if p.refs.Add(-1) != 0 {
		return
	}
	p.once.Do(func() { close(p.stop) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.repo.ReleasePoints(ctx, p.ID); err != nil {
		logger.LegacyPrintf("service.points_billing", "Release reservation %s failed: %v", p.ID, err)
	}
}
func (p *PointsPayment) HandlerDone() { p.handlerOnce.Do(p.done) }

func (s *BillingCacheService) BillingEnabled() bool {
	return s != nil && (s.cfg == nil || s.cfg.RunMode != config.RunModeSimple)
}

func (s *BillingCacheService) HasPoints(ctx context.Context, userID int64, group *Group) (bool, error) {
	if !s.BillingEnabled() || group == nil || !group.AllowPointsBilling {
		return false, nil
	}
	if err := ValidateGroupPointsRate(true, group.PointsPerUSD); err != nil {
		return false, err
	}
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return false, ErrBillingServiceUnavailable
	}
	amount, err := repo.PointsAvailable(ctx, userID)
	return amount > 0, err
}

func (s *BillingCacheService) CheckMoneyEligibility(ctx context.Context, userID int64) error {
	return s.checkBalanceEligibility(ctx, userID)
}

func (s *BillingCacheService) ReservePointsPayment(ctx context.Context, key *APIKey, estimate float64) (context.Context, func(), bool, error) {
	if !s.BillingEnabled() || key == nil || key.User == nil || key.Group == nil || !key.Group.AllowPointsBilling {
		return ctx, func() {}, false, nil
	}
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return ctx, func() {}, false, ErrBillingServiceUnavailable
	}
	id := uuid.NewString()
	rate, err := repo.ReservePoints(ctx, id, key.User.ID, key.ID, key.Group.ID, estimate)
	if err != nil {
		return ctx, func() {}, false, err
	}
	p := &PointsPayment{ID: id, Rate: rate, UserID: key.User.ID, APIKeyID: key.ID, GroupID: key.Group.ID, repo: repo, stop: make(chan struct{})}
	p.refs.Store(1)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := repo.RenewPoints(c, p.ID); err != nil {
					logger.LegacyPrintf("service.points_billing", "Renew reservation %s failed: %v", p.ID, err)
				}
				cancel()
			}
		}
	}()
	return context.WithValue(ctx, pointsPaymentContextKey{}, p), p.HandlerDone, true, nil
}

func (s *BillingCacheService) startPointsRecovery() {
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.pointsRecoveryStop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				var err error
				if recovery, ok := repo.(pointsRecoveryWithEffects); ok {
					err = recovery.RecoverPointsWithEffects(ctx, func(cmd *UsageBillingCommand) { s.recoveredPointsEffects(ctx, cmd) })
				} else {
					err = repo.RecoverPoints(ctx)
				}
				if err != nil {
					logger.LegacyPrintf("service.points_billing", "Recover settlements failed: %v", err)
				}
				cancel()
			}
		}
	}()
}

func (s *BillingCacheService) recoveredPointsEffects(ctx context.Context, cmd *UsageBillingCommand) {
	if s.cache != nil {
		if err := s.InvalidateAPIKeyRateLimit(ctx, cmd.APIKeyID); err != nil {
			logger.LegacyPrintf("service.points_billing", "Invalidate recovered key %d rate limits failed: %v", cmd.APIKeyID, err)
		}
	}
	if cmd.PointsQuotaPlatform == "" || cmd.PointsCostUSD <= 0 || s.userPlatformQuotaRepo == nil {
		return
	}
	if s.HasUserPlatformQuotaLimit(ctx, cmd.UserID, cmd.PointsQuotaPlatform) {
		s.IncrementUserPlatformQuotaUsage(cmd.UserID, cmd.PointsQuotaPlatform, cmd.PointsCostUSD)
		if s.cfg == nil || !s.cfg.Database.UserPlatformQuotaFlusherEnabled {
			if err := s.userPlatformQuotaRepo.IncrementUsageWithReset(ctx, cmd.UserID, cmd.PointsQuotaPlatform, cmd.PointsCostUSD, time.Now().UTC()); err != nil {
				logger.LegacyPrintf("service.points_billing", "Persist recovered platform quota user=%d platform=%s failed: %v", cmd.UserID, cmd.PointsQuotaPlatform, err)
			}
		}
	}
}
