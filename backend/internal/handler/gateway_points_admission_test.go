//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPointsAdmission_SmartGroupUsesSelectedWallet(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		rate          float64
		points, money float64
		body          string
		path          string
		want          bool
		wantErr       error
	}{
		{"points cannot fall back to money", true, 100, 0, 100, "{}", "/v1/responses", false, nil},
		{"points debt blocks group", true, 100, -70, 100, "{}", "/v1/responses", false, nil},
		{"money group ignores points", false, 0, 0, 100, "{}", "/v1/responses", true, nil},
		{"money group needs money", false, 0, 100, 0, "{}", "/v1/responses", false, nil},
		{"points group with own rate", true, 250, 1, 0, "{}", "/v1/responses", true, nil},
		{"invalid group rate", true, 0, 1, 100, "{}", "/v1/responses", false, service.ErrInvalidPointsRate},
		{"images use money", true, 0, 0, 100, "{}", "/v1/images/generations", true, nil},
		{"image tool uses money", true, 0, 0, 100, `{"tools":[{"type":"image_generation"}]}`, "/v1/responses", true, nil},
		{"alpha search uses points", true, 100, 0, 100, "{}", "/v1/alpha/search", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pointsAdmissionRepo{credit: tc.points}
			cfg := &config.Config{}
			billing := service.NewBillingCacheService(newHandlerInflightCache(tc.money), repo, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := &GatewayHandler{billingCacheService: billing, cfg: cfg}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tc.path, nil)
			key := &service.APIKey{UserID: 1, User: &service.User{ID: 1, Balance: tc.money}}
			group := &service.Group{ID: 16, AllowPointsBilling: tc.enabled, PointsPerUSD: tc.rate}
			allowed, sub, err := h.admitSmartGroup(c, key, group, nil, tokenInflightEstimate("gpt-5", []byte(tc.body)))
			require.Equal(t, tc.want, allowed)
			require.Nil(t, sub)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type pointsAdmissionRepo struct {
	service.UserRepository
	service.PointsRepository
	credit             float64
	reserves, releases int
}

func (r *pointsAdmissionRepo) ReservePoints(context.Context, string, int64, int64, int64, float64) (float64, error) {
	r.reserves++
	if r.credit <= 0 {
		return 0, service.ErrInsufficientPoints
	}
	return 100, nil
}
func (r *pointsAdmissionRepo) ReleasePoints(context.Context, string) error { r.releases++; return nil }
func (r *pointsAdmissionRepo) PointsAvailable(context.Context, int64) (float64, error) {
	return r.credit, nil
}

func TestPointsAdmission_GroupWalletEligibility(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		points, money float64
		wantErr       error
	}{
		{"disabled ignores points and uses money", false, 100, 10, nil},
		{"disabled rejects empty money wallet", false, 100, 0, service.ErrInsufficientBalance},
		{"enabled rejects empty points wallet", true, 0, 100, service.ErrInsufficientPoints},
		{"enabled rejects points debt", true, -70, 100, service.ErrInsufficientPoints},
		{"enabled accepts points without money", true, 1, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pointsAdmissionRepo{credit: tc.points}
			billing := service.NewBillingCacheService(newHandlerInflightCache(tc.money), repo, nil, nil, nil, nil, &config.Config{}, nil)
			t.Cleanup(billing.Stop)
			user := &service.User{ID: 1, Balance: tc.money}
			group := &service.Group{ID: 3, AllowPointsBilling: tc.enabled, PointsPerUSD: 100}
			key := &service.APIKey{ID: 2, User: user, Group: group}
			err := billing.CheckBillingEligibility(context.Background(), user, key, group, nil, "")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			if !tc.enabled && tc.wantErr == nil {
				ctx, done, err := reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{cost: 1, priced: true}, key, nil, tokenInflightEstimate("text", nil))
				require.NoError(t, err)
				require.Nil(t, service.PointsPaymentFromContext(ctx))
				done()
				require.Zero(t, repo.reserves)
			}
		})
	}
}

func TestPointsAdmission_MediaUsesMoneyWithoutPoints(t *testing.T) {
	repo := &pointsAdmissionRepo{}
	billing := service.NewBillingCacheService(newHandlerInflightCache(10), repo, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	key := &service.APIKey{User: &service.User{ID: 1}, Group: &service.Group{ID: 3, AllowPointsBilling: true}}
	req := tokenInflightEstimate("model", []byte(`{"tools":[{"type":"image_generation"}]}`))
	require.NoError(t, billing.CheckBillingEligibility(context.Background(), key.User, key, key.Group, nil, "", req))
	ctx, done, err := reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{cost: 1, priced: true}, key, nil, req)
	require.NoError(t, err)
	require.Nil(t, service.PointsPaymentFromContext(ctx))
	done()
	require.Zero(t, repo.reserves)
}

func TestPointsAdmission_WalletSelectionAndAsyncLifetime(t *testing.T) {
	repo := &pointsAdmissionRepo{credit: 30}
	cache := newHandlerInflightCache(10)
	billing := service.NewBillingCacheService(cache, repo, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	key := &service.APIKey{ID: 2, User: &service.User{ID: 1}, Group: &service.Group{ID: 3, AllowPointsBilling: true, PointsPerUSD: 100}}
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
	// No remaining points rejects even when the money wallet has a balance.
	repo.credit = -70
	key.User.Balance = 100
	ctx, done, err = reserveInflightBalanceCtx(context.Background(), billing, &countingEstimator{}, key, nil, tokenInflightEstimate("text", nil))
	require.ErrorIs(t, err, service.ErrInsufficientPoints)
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
