package service

import "testing"

func TestAPIKeyBillingSnapshotForAccountUsesSelectedSmartGroup(t *testing.T) {
	standard := &Group{ID: 11, Name: "cheap", RateMultiplier: 0.5}
	selected := &Group{ID: 22, Name: "fast", RateMultiplier: 1.2}
	key := &APIKey{
		RoutingMode:   "smart",
		GroupID:       &standard.ID,
		Group:         standard,
		SmartGroupIDs: []int64{standard.ID, selected.ID},
		SmartGroups:   []*Group{standard, selected},
	}
	account := &Account{ID: 7, RoutingGroupID: selected.ID}

	snapshot := key.BillingSnapshotForAccount(account)
	if snapshot == key {
		t.Fatal("smart billing should use a request-local snapshot")
	}
	if snapshot.GroupID == nil || *snapshot.GroupID != selected.ID {
		t.Fatalf("selected group id = %v, want %d", snapshot.GroupID, selected.ID)
	}
	if snapshot.Group != selected {
		t.Fatalf("selected group pointer was not preserved")
	}
	if key.Group != standard || key.GroupID == nil || *key.GroupID != standard.ID {
		t.Fatal("original API key was mutated")
	}
}

func TestAPIKeyBillingSnapshotForAccountDoesNotInferSharedMembership(t *testing.T) {
	first := &Group{ID: 11}
	second := &Group{ID: 22}
	key := &APIKey{
		RoutingMode:   "smart",
		GroupID:       &first.ID,
		Group:         first,
		SmartGroupIDs: []int64{first.ID, second.ID},
		SmartGroups:   []*Group{first, second},
	}
	account := &Account{ID: 8, GroupIDs: []int64{second.ID}}

	snapshot := key.BillingSnapshotForAccount(account)
	if snapshot != key || *snapshot.GroupID != first.ID {
		t.Fatal("billing must not infer a group from shared account membership")
	}
}
