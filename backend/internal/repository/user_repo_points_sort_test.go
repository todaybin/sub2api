//go:build unit

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserRepositoryPointsSortAcrossPages(t *testing.T) {
	repo, _ := newUserEntRepo(t)
	ctx := context.Background()
	var ids []int64
	for i, points := range []float64{50, -0.5, 0, 50, 0.25} {
		user := &service.User{
			Email: fmt.Sprintf("points-%d@example.com", i), PasswordHash: "hash",
			Role: service.RoleUser, Status: service.StatusActive, PointsBalance: points,
		}
		require.NoError(t, repo.Create(ctx, user))
		ids = append(ids, user.ID)
	}
	require.NoError(t, repo.Create(ctx, &service.User{
		Email: "disabled@example.com", PasswordHash: "hash", Role: service.RoleUser,
		Status: service.StatusDisabled, PointsBalance: 1000,
	}))
	for _, tc := range []struct {
		order string
		want  []int64
	}{
		{"asc", []int64{ids[1], ids[2], ids[4], ids[0], ids[3]}},
		{"desc", []int64{ids[3], ids[0], ids[4], ids[2], ids[1]}},
	} {
		t.Run(tc.order, func(t *testing.T) {
			var got []int64
			for page := 1; page <= 3; page++ {
				users, result, err := repo.ListWithFilters(ctx, pagination.PaginationParams{
					Page: page, PageSize: 2, SortBy: "points_balance", SortOrder: tc.order,
				}, service.UserListFilters{Status: service.StatusActive})
				require.NoError(t, err)
				require.EqualValues(t, 5, result.Total)
				for _, user := range users {
					got = append(got, user.ID)
				}
			}
			require.Equal(t, tc.want, got, "sort before pagination, including negative, zero, fractional and tied balances")
		})
	}
}
