package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type smartBillingSubscriptionRepo struct {
	service.UserSubscriptionRepository
	err error
}

func (r smartBillingSubscriptionRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	return nil, r.err
}

func TestSmartBillingSchemaReportsPerGroupAndUnavailableSubscription(t *testing.T) {
	rate := 0.5
	h := newKeyBillingHandler(&keyBillingUserGroupRateRepo{rate: &rate})
	h.smartSubscriptions = service.NewSubscriptionService(nil, smartBillingSubscriptionRepo{err: service.ErrSubscriptionNotFound}, nil, nil, nil)
	key := &service.APIKey{ID: 9, UserID: 1, User: &service.User{ID: 1, Balance: 10}, RoutingMode: "smart", RoutingStrategy: "price", SmartGroups: []*service.Group{
		{ID: 1, Name: "balance", Platform: service.PlatformOpenAI, RateMultiplier: 2, ImageRateIndependent: true, ImageRateMultiplier: 3},
		{ID: 2, Name: "subscription", Platform: service.PlatformGemini, RateMultiplier: 1, SubscriptionType: service.SubscriptionTypeSubscription},
	}}
	c, w := newKeyBillingContext(key)
	h.KeyBillingInfo(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var got struct {
		Schema int    `json:"schema_version"`
		Scope  string `json:"billing_scope"`
		Groups []struct {
			Available bool   `json:"available"`
			Mode      string `json:"billing_mode"`
			Pricing   struct {
				Token keyBillingInfoResponse `json:"token"`
				Image struct {
					Rate float64 `json:"rate_multiplier"`
				} `json:"image"`
			} `json:"pricing"`
		} `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, 2, got.Schema)
	require.Equal(t, "selected_group", got.Scope)
	require.Len(t, got.Groups, 2)
	require.True(t, got.Groups[0].Available)
	require.Equal(t, 0.5, got.Groups[0].Pricing.Token.ResolvedRateMultiplier)
	require.Equal(t, 3.0, got.Groups[0].Pricing.Image.Rate)
	require.False(t, got.Groups[1].Available)
	require.Equal(t, "subscription", got.Groups[1].Pricing.Token.BillingMode)
	require.Nil(t, key.GroupID)

	key.User.Balance = -10
	c, w = newKeyBillingContext(key)
	h.KeyBillingInfo(c)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.False(t, got.Groups[0].Available)
	h.smartSubscriptions = service.NewSubscriptionService(nil, smartBillingSubscriptionRepo{err: errors.New("repository unavailable")}, nil, nil, nil)
	c, w = newKeyBillingContext(key)
	h.KeyBillingInfo(c)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
