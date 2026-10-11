//go:build unit

package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func pointerOf[T any](value T) *T { return &value }

func TestGroupPointsRateValidation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, rate := range []float64{-1, 1e-9, 1e12, math.NaN(), math.Inf(1)} {
			require.ErrorIs(t, ValidateGroupPointsRate(enabled, rate), ErrInvalidPointsRate)
		}
		for _, rate := range []float64{1e-8, 100, 999999999999} {
			require.NoError(t, ValidateGroupPointsRate(enabled, rate))
		}
	}
	require.NoError(t, ValidateGroupPointsRate(false, 0))
	require.ErrorIs(t, ValidateGroupPointsRate(true, 0), ErrInvalidPointsRate)
}

func TestCreateGroupPointsRejectsMissingRateWithoutGlobalSettings(t *testing.T) {
	svc := &adminServiceImpl{}
	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{AllowPointsBilling: true})
	require.ErrorIs(t, err, ErrInvalidPointsRate)
}

type pointsGroupCreateRepo struct {
	GroupRepository
	created *Group
}

func (r *pointsGroupCreateRepo) Create(_ context.Context, group *Group) error {
	r.created = group
	group.ID = 16
	return nil
}

func TestCreateGroupPointsPersistsOwnRateWithoutGlobalSettings(t *testing.T) {
	repo := &pointsGroupCreateRepo{}
	svc := &adminServiceImpl{groupRepo: repo}
	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "points", Platform: PlatformOpenAI, RateMultiplier: 1,
		AllowPointsBilling: true, PointsPerUSD: 250.123456789,
	})
	require.NoError(t, err)
	require.Equal(t, repo.created, group)
	require.True(t, group.AllowPointsBilling)
	require.Equal(t, 250.12345679, group.PointsPerUSD)
}

func TestUpdateGroupPointsUsesMergedGroupConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enabled     bool
		rate        float64
		input       UpdateGroupInput
		wantRate    float64
		wantEnabled bool
		wantErr     bool
	}{
		{"enable without rate", false, 0, UpdateGroupInput{AllowPointsBilling: pointerOf(true)}, 0, false, true},
		{"enable with group rate", false, 0, UpdateGroupInput{AllowPointsBilling: pointerOf(true), PointsPerUSD: pointerOf(100.123456789)}, 100.12345679, true, false},
		{"omitted rate preserved", true, 100, UpdateGroupInput{Name: "renamed"}, 100, true, false},
		{"clear active rate rejected", true, 100, UpdateGroupInput{PointsPerUSD: pointerOf(0.0)}, 100, true, true},
		{"disable missing rate", true, 0, UpdateGroupInput{AllowPointsBilling: pointerOf(false)}, 0, false, false},
		{"disabled group retains rate", true, 200, UpdateGroupInput{AllowPointsBilling: pointerOf(false)}, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &groupPlatformRepoStub{group: &Group{ID: 16, Platform: PlatformOpenAI, AllowPointsBilling: tc.enabled, PointsPerUSD: tc.rate}}
			svc := &adminServiceImpl{groupRepo: repo}
			group, err := svc.UpdateGroup(context.Background(), 16, &tc.input)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidPointsRate)
				require.Nil(t, repo.updated)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantRate, group.PointsPerUSD)
			require.Equal(t, tc.wantEnabled, group.AllowPointsBilling)
		})
	}
}

func TestDuplicateGroupPreservesPointsConfiguration(t *testing.T) {
	group := cloneGroupForDuplicate(&Group{AllowPointsBilling: true, PointsPerUSD: 250}, "operation")
	require.True(t, group.AllowPointsBilling)
	require.Equal(t, 250.0, group.PointsPerUSD)
}

func TestGroupPointsRateSurvivesAuthSnapshot(t *testing.T) {
	svc := &APIKeyService{}
	key := &APIKey{ID: 2, UserID: 1, User: &User{ID: 1}, Group: &Group{ID: 16, AllowPointsBilling: true, PointsPerUSD: 250}}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	require.Equal(t, 250.0, snapshot.Group.PointsPerUSD)
	restored := svc.snapshotToAPIKey("key", snapshot)
	require.True(t, restored.Group.AllowPointsBilling)
	require.Equal(t, 250.0, restored.Group.PointsPerUSD)
}
