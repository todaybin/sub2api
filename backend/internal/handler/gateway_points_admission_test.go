//go:build unit

package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type pointsAdmissionRepo struct {
	service.UserRepository
	service.PointsRepository
	credit             float64
	reserves, releases int
}

func (r *pointsAdmissionRepo) ReservePoints(context.Context, string, int64, int64, int64, float64) (float64, error) {
	r.reserves++
	if r.credit <= 0 {
		return 0, service.ErrInsufficientBalance
	}
	return 100, nil
}
func (r *pointsAdmissionRepo) ReleasePoints(context.Context, string) error { r.releases++; return nil }

func TestPointsAdmission_WalletSelectionAndAsyncLifetime(t *testing.T) {
	repo := &pointsAdmissionRepo{credit: 30}
	cache := newHandlerInflightCache(10)
	billing := service.NewBillingCacheService(cache, repo, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	key := &service.APIKey{ID: 2, User: &service.User{ID: 1}, Group: &service.Group{ID: 3, AllowPointsBilling: true}}
	ctx, done, err := reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{cost: 1, priced: true}, key, nil, tokenInflightEstimate("text", nil))
	require.NoError(t, err)
	// Points work even when the optional Redis money reservation is disabled.
	payment := service.PointsPaymentFromContext(ctx)
	require.NotNil(t, payment)
	require.Equal(t, 100.0, payment.Rate)
	finishTask := payment.Acquire()
	done()
	require.Zero(t, repo.releases)
	finishTask()
	finishTask()
	require.Equal(t, 1, repo.releases)
	// No remaining points selects the existing money wallet.
	repo.credit = -70
	ctx, done, err = reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{}, key, nil, tokenInflightEstimate("text", nil))
	require.NoError(t, err)
	require.Nil(t, service.PointsPaymentFromContext(ctx))
	done()
}

func TestPointsAdmission_SubscriptionAndSimpleModeSkipPoints(t *testing.T) {
	for _, simple := range []bool{false, true} {
		cfg := &config.Config{}
		if simple {
			cfg.RunMode = config.RunModeSimple
		}
		repo := &pointsAdmissionRepo{credit: 30}
		billing := service.NewBillingCacheService(nil, repo, nil, nil, nil, nil, cfg, nil)
		t.Cleanup(billing.Stop)
		key := &service.APIKey{User: &service.User{ID: 1}, Group: &service.Group{AllowPointsBilling: true, SubscriptionType: service.SubscriptionTypeSubscription}}
		var subscription *service.UserSubscription
		if !simple {
			subscription = &service.UserSubscription{}
		}
		est := &countingEstimator{}
		_, done, err := reserveInflightBalanceCtx(context.Background(), billing, est, key, subscription, tokenInflightEstimate("text", nil))
		require.NoError(t, err)
		done()
		require.Zero(t, repo.reserves)
		require.Zero(t, est.calls)
	}
}

func TestPointsAdmission_ExplicitMediaRequiresMoney(t *testing.T) {
	repo := &pointsAdmissionRepo{credit: 30}
	billing := service.NewBillingCacheService(newHandlerInflightCache(0), repo, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	key := &service.APIKey{User: &service.User{ID: 1}, Group: &service.Group{AllowPointsBilling: true}}
	req := tokenInflightEstimate("gpt-5", []byte(`{"tools":[{"type":"image_generation"}]}`))
	require.Equal(t, service.InflightEstimateImage, req.Kind)
	_, _, err := reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{cost: 1, priced: true}, key, nil, req)
	require.ErrorIs(t, err, service.ErrInsufficientBalance)
	require.Zero(t, repo.reserves)
}
