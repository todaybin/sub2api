//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type smartCRUDKeyRepo struct {
	APIKeyRepository
	key     *APIKey
	updated *APIKey
	fields  APIKeyUpdateFields
}

func (r *smartCRUDKeyRepo) GetByID(context.Context, int64) (*APIKey, error) { return r.key, nil }
func (r *smartCRUDKeyRepo) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	r.updated, r.fields = key, fields
	return nil
}

type smartCRUDGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *smartCRUDGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	return r.groups[id], nil
}

func TestSmartKeyUpdateNormalizesAndClearsSingleGroup(t *testing.T) {
	group := &Group{ID: 9, Status: StatusActive}
	key := &APIKey{ID: 1, UserID: 7, GroupID: &group.ID, Group: group, RoutingMode: "single"}
	repo := &smartCRUDKeyRepo{key: key}
	svc := &APIKeyService{apiKeyRepo: repo, userRepo: &visibilityUserRepo{user: &User{ID: 7}}, groupRepo: &smartCRUDGroupRepo{groups: map[int64]*Group{9: group}}}
	mode, strategy := " SMART ", " PRICE "
	ids := []int64{9}
	result, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{RoutingMode: &mode, RoutingStrategy: &strategy, SmartGroupIDs: &ids})
	require.NoError(t, err)
	require.Equal(t, "smart", result.RoutingMode)
	require.Equal(t, "price", result.RoutingStrategy)
	require.Equal(t, ids, result.SmartGroupIDs)
	require.Nil(t, result.GroupID)
	require.Nil(t, result.Group)
	require.Equal(t, APIKeyUpdateFields{Routing: true, GroupID: true}, repo.fields)
	require.Equal(t, "single", key.RoutingMode)
	require.Same(t, group, key.Group)
	ids[0] = 100
	require.Equal(t, []int64{9}, result.SmartGroupIDs)
}

func TestSmartKeyUpdateSingleClearsSmartConfiguration(t *testing.T) {
	group := &Group{ID: 9, Status: StatusActive}
	key := &APIKey{ID: 1, UserID: 7, RoutingMode: "smart", RoutingStrategy: "speed", SmartGroupIDs: []int64{9}, SmartGroups: []*Group{group}}
	repo := &smartCRUDKeyRepo{key: key}
	svc := &APIKeyService{apiKeyRepo: repo, userRepo: &visibilityUserRepo{user: &User{ID: 7}}, groupRepo: &smartCRUDGroupRepo{groups: map[int64]*Group{9: group}}}
	mode := "single"
	result, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{RoutingMode: &mode, GroupID: &group.ID})
	require.NoError(t, err)
	require.Equal(t, "single", result.RoutingMode)
	require.Empty(t, result.SmartGroupIDs)
	require.Empty(t, result.SmartGroups)
	require.Same(t, group, result.Group)
	require.Equal(t, group.ID, *result.GroupID)
	require.Equal(t, "smart", key.RoutingMode)
	require.Nil(t, key.GroupID)
}

func TestSmartKeyUnauthorizedUpdateDoesNotMutateSharedKey(t *testing.T) {
	group := &Group{ID: 9, Status: StatusActive, IsExclusive: true}
	key := &APIKey{ID: 1, UserID: 7, Name: "before", RoutingMode: "single"}
	repo := &smartCRUDKeyRepo{key: key}
	svc := &APIKeyService{apiKeyRepo: repo, userRepo: &visibilityUserRepo{user: &User{ID: 7}}, groupRepo: &smartCRUDGroupRepo{groups: map[int64]*Group{9: group}}}
	mode, name := "smart", "changed"
	ids := []int64{9}
	_, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{Name: &name, RoutingMode: &mode, SmartGroupIDs: &ids})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
	require.Nil(t, repo.updated)
	require.Equal(t, "before", key.Name)
	require.Equal(t, "single", key.RoutingMode)
	require.Empty(t, key.SmartGroupIDs)
}
