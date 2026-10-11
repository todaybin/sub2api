//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pointsAdjustmentRecord struct {
	amount           float64
	operation, notes string
}

type pointsHandlerRepo struct {
	service.UserRepository
	service.PointsRepository
	user        service.User
	adjustments map[string]pointsAdjustmentRecord
	entries     []service.PointsLedgerEntry
}

func (r *pointsHandlerRepo) GetByID(context.Context, int64) (*service.User, error) {
	user := r.user
	return &user, nil
}

func (r *pointsHandlerRepo) AdjustPoints(_ context.Context, id int64, amount float64, operation, operationID, notes string) (service.BalanceChange, error) {
	if id != r.user.ID {
		return service.BalanceChange{}, service.ErrUserNotFound
	}
	record := pointsAdjustmentRecord{amount, operation, notes}
	if prior, ok := r.adjustments[operationID]; ok {
		if prior != record {
			return service.BalanceChange{}, service.ErrPointsOperationConflict
		}
		return service.BalanceChange{}, nil
	}
	old := r.user.PointsBalance
	switch operation {
	case "add":
		r.user.PointsBalance += amount
	case "subtract":
		r.user.PointsBalance -= amount
	case "set":
		r.user.PointsBalance = amount
	}
	r.adjustments[operationID] = record
	r.entries = append(r.entries, service.PointsLedgerEntry{ID: int64(len(r.entries) + 1), Kind: "adjustment", Amount: r.user.PointsBalance - old, BalanceAfter: r.user.PointsBalance, Notes: notes})
	return service.BalanceChange{Old: old, New: r.user.PointsBalance}, nil
}

func (r *pointsHandlerRepo) ListPointsLedger(_ context.Context, id int64, page, size int) ([]service.PointsLedgerEntry, int64, error) {
	if id != r.user.ID {
		return nil, 0, service.ErrUserNotFound
	}
	return r.entries, int64(len(r.entries)), nil
}

func pointsHandlerRouter(app string, repo *pointsHandlerRepo) *gin.Engine {
	h := &UserHandler{userService: service.NewUserService(repo, nil, nil, nil)}
	r := gin.New()
	if app != "" {
		r.Use(func(c *gin.Context) {
			c.Set(middleware.IntegrationAuthenticatedContextKey, true)
			c.Set(middleware.IntegrationAppIDContextKey, app)
		})
	}
	r.POST("/users/:id/points", h.UpdatePoints)
	r.GET("/users/:id/points-history", h.PointsHistory)
	return r
}

func newPointsHandlerRepo() *pointsHandlerRepo {
	return &pointsHandlerRepo{user: service.User{ID: 7, PointsBalance: -70}, adjustments: make(map[string]pointsAdjustmentRecord)}
}

func TestIntegrationPointsTopUpAndRetry(t *testing.T) {
	setupIntegrationIdempotency(t)
	repo := newPointsHandlerRepo()
	r := pointsHandlerRouter("app-a", repo)
	body := `{"points":100,"operation":"add","notes":"order-1001"}`
	first := accountRequest(r, "/users/7/points", body, "order-1001")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var result struct {
		Data struct {
			ID            int64   `json:"id"`
			PointsBalance float64 `json:"points_balance"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &result))
	require.Equal(t, int64(7), result.Data.ID)
	require.Equal(t, float64(30), result.Data.PointsBalance, "top-up must offset points debt")
	retry := accountRequest(r, "/users/7/points", body, "order-1001")
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), retry.Body.String())
	for _, changed := range []string{
		`{"points":200,"operation":"add","notes":"order-1001"}`,
		`{"points":100,"operation":"add","notes":"changed"}`,
	} {
		conflict := accountRequest(r, "/users/7/points", changed, "order-1001")
		require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	}
	require.Len(t, repo.adjustments, 1)

	// Loss or expiry of coordinator records must not create a second ledger credit.
	setupIntegrationIdempotency(t)
	durableRetry := accountRequest(r, "/users/7/points", body, " order-1001 ")
	require.Equal(t, http.StatusOK, durableRetry.Code, durableRetry.Body.String())
	require.Equal(t, float64(30), repo.user.PointsBalance)
	require.Len(t, repo.adjustments, 1)

	history := httptest.NewRecorder()
	r.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/users/7/points-history?page=1&page_size=20", nil))
	require.Equal(t, http.StatusOK, history.Code, history.Body.String())
	require.Contains(t, history.Body.String(), `"amount":100`)
	require.Contains(t, history.Body.String(), `"balance_after":30`)
	require.Contains(t, history.Body.String(), `"total":1`)
}

func TestIntegrationPointsTopUpScopesLedgerByApp(t *testing.T) {
	setupIntegrationIdempotency(t)
	repo := newPointsHandlerRepo()
	for _, app := range []string{"app-a", "app-b", ""} {
		r := pointsHandlerRouter(app, repo)
		result := accountRequest(r, "/users/7/points", `{"points":100,"operation":"add"}`, "same-order")
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	}
	require.Equal(t, float64(230), repo.user.PointsBalance)
	require.Len(t, repo.adjustments, 3)
	require.Contains(t, repo.adjustments, "admin:7:same-order", "preserve existing admin ledger IDs")
}

func TestIntegrationPointsTopUpRejectsInvalidRequests(t *testing.T) {
	setupIntegrationIdempotency(t)
	repo := newPointsHandlerRepo()
	r := pointsHandlerRouter("app-a", repo)
	for _, tc := range []struct{ path, body, key string }{
		{"/users/7/points", `{"points":100,"operation":"add"}`, ""},
		{"/users/7/points", `{"points":-1,"operation":"add"}`, "negative"},
		{"/users/7/points", `{"points":1000000000000,"operation":"add"}`, "too-large"},
		{"/users/7/points", `{"points":100,"operation":"invalid"}`, "invalid-operation"},
		{"/users/0/points", `{"points":100,"operation":"add"}`, "zero-id"},
		{"/users/-1/points", `{"points":100,"operation":"add"}`, "negative-id"},
	} {
		result := accountRequest(r, tc.path, tc.body, tc.key)
		require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
	}
	notFound := accountRequest(r, "/users/8/points", `{"points":100,"operation":"add"}`, "missing-user")
	require.Equal(t, http.StatusNotFound, notFound.Code, notFound.Body.String())
	require.Empty(t, repo.adjustments)
	service.SetDefaultIdempotencyCoordinator(nil)
	unavailable := accountRequest(r, "/users/7/points", `{"points":100,"operation":"add"}`, "no-store")
	require.Equal(t, http.StatusServiceUnavailable, unavailable.Code, unavailable.Body.String())
	require.Empty(t, repo.adjustments)
}
