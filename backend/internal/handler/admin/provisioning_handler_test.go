//go:build unit

package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type provisioningAPIKeyServiceStub struct {
	created []service.CreateAPIKeyRequest
	deleted []int64
}

type provisioningAdminServiceStub struct {
	*stubAdminService
	createdUsers int
	updateInput  *service.UpdateUserInput
}

func (s *provisioningAdminServiceStub) CreateUser(ctx context.Context, input *service.CreateUserInput) (*service.User, error) {
	s.createdUsers++
	return s.stubAdminService.CreateUser(ctx, input)
}

func (s *provisioningAdminServiceStub) UpdateUser(_ context.Context, id int64, input *service.UpdateUserInput) (*service.User, error) {
	s.updateInput = input
	for i := range s.users {
		if s.users[i].ID != id {
			continue
		}
		if input.AllowedGroups != nil {
			s.users[i].AllowedGroups = append([]int64(nil), (*input.AllowedGroups)...)
		}
		if input.Notes != nil {
			s.users[i].Notes = *input.Notes
		}
		result := s.users[i]
		return &result, nil
	}
	return s.stubAdminService.UpdateUser(context.Background(), id, input)
}

func (s *provisioningAPIKeyServiceStub) Create(_ context.Context, userID int64, req service.CreateAPIKeyRequest) (*service.APIKey, error) {
	s.created = append(s.created, req)
	id := int64(100 + len(s.created))
	return &service.APIKey{ID: id, UserID: userID, Name: req.Name, GroupID: req.GroupID, Key: "sk-created", Status: service.StatusActive}, nil
}

func (s *provisioningAPIKeyServiceStub) Delete(_ context.Context, id int64, _ int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func TestNormalizeProvisionAPIKeysSupportsLegacyAndBatch(t *testing.T) {
	group1, group2 := int64(1), int64(2)
	items, err := normalizeProvisionAPIKeys(ProvisionUserRequest{
		ExternalID: "workmesh:site-100",
		APIKey:     &ProvisioningAPIKeyRequest{Name: "legacy", GroupID: &group1},
		APIKeys:    []ProvisioningAPIKeyRequest{{Name: "batch", GroupID: &group2}},
	})
	if err != nil {
		t.Fatalf("normalizeProvisionAPIKeys() error = %v", err)
	}
	if len(items) != 2 || items[0].Name != "legacy" || items[1].Name != "batch" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestNormalizeProvisionAPIKeysRejectsDuplicateGroup(t *testing.T) {
	groupID := int64(12)
	_, err := normalizeProvisionAPIKeys(ProvisionUserRequest{APIKeys: []ProvisioningAPIKeyRequest{
		{Name: "first", GroupID: &groupID},
		{Name: "second", GroupID: &groupID},
	}})
	if err == nil {
		t.Fatal("expected duplicate group error")
	}
}

func TestNormalizeProvisionAPIKeysRejectsUnsafeExternalID(t *testing.T) {
	_, err := normalizeProvisionAPIKeys(ProvisionUserRequest{
		ExternalID: "site-1]\n[integration_external_id:site-2",
		APIKeys:    []ProvisioningAPIKeyRequest{{Name: "default"}},
	})
	if err == nil {
		t.Fatal("expected unsafe external_id error")
	}
}

func TestEnsureAPIKeysReusesExistingGroupWithoutReturningSecret(t *testing.T) {
	group1, group2 := int64(1), int64(2)
	adminService := newStubAdminService()
	adminService.apiKeys = []service.APIKey{{ID: 10, UserID: 1, Name: "existing", GroupID: &group1, Key: "sk-existing", Status: service.StatusActive}}
	keyService := &provisioningAPIKeyServiceStub{}
	handler := &ProvisioningHandler{adminService: adminService, apiKeyService: keyService}

	responses, ids, err := handler.ensureAPIKeys(context.Background(), 1, []ProvisioningAPIKeyRequest{
		{Name: "existing", GroupID: &group1},
		{Name: "new", GroupID: &group2},
	})
	if err != nil {
		t.Fatalf("ensureAPIKeys() error = %v", err)
	}
	if len(responses) != 2 || responses[0].Created || responses[0].Key != "" {
		t.Fatalf("existing key leaked or marked created: %#v", responses[0])
	}
	if !responses[1].Created || responses[1].Key == "" || len(ids) != 1 {
		t.Fatalf("new key was not returned once: responses=%#v ids=%#v", responses, ids)
	}
	if len(keyService.created) != 1 || keyService.created[0].GroupID == nil || *keyService.created[0].GroupID != group2 {
		t.Fatalf("unexpected created keys: %#v", keyService.created)
	}
}

func TestFindExistingUserPrefersExternalID(t *testing.T) {
	adminService := newStubAdminService()
	adminService.users = []service.User{
		{ID: 1, Email: "old@example.com", Role: service.RoleUser, Notes: "[integration_external_id:workmesh:site-9]"},
		{ID: 2, Email: "workmesh-site-9@example.com", Role: service.RoleUser},
	}
	handler := &ProvisioningHandler{adminService: adminService}

	user, err := handler.findExistingUser(context.Background(), "workmesh:site-9", "workmesh-site-9@example.com")
	if err != nil {
		t.Fatalf("findExistingUser() error = %v", err)
	}
	if user == nil || user.ID != 1 {
		t.Fatalf("findExistingUser() = %#v, want external ID owner", user)
	}
	if adminService.lastListUsers.calls != 1 {
		t.Fatalf("email fallback should not run after external ID match, calls = %d", adminService.lastListUsers.calls)
	}
}

func TestEnsureUserClaimsEmailAndPreservesExistingData(t *testing.T) {
	base := newStubAdminService()
	base.users = []service.User{{
		ID: 7, Email: "workmesh-site-12@example.com", Role: service.RoleUser,
		Notes: "人工备注", AllowedGroups: []int64{3},
	}}
	adminService := &provisioningAdminServiceStub{stubAdminService: base}
	handler := &ProvisioningHandler{adminService: adminService}
	groupID := int64(8)

	user, created, previous, err := handler.ensureUser(context.Background(), ProvisionUserRequest{
		ExternalID: "workmesh:site-12", Email: "workmesh-site-12@example.com",
	}, []ProvisioningAPIKeyRequest{{Name: "group-8", GroupID: &groupID}}, 99)
	if err != nil {
		t.Fatalf("ensureUser() error = %v", err)
	}
	if created || adminService.createdUsers != 0 || user.ID != 7 {
		t.Fatalf("existing email account was not claimed: created=%v calls=%d user=%#v", created, adminService.createdUsers, user)
	}
	if previous == nil || previous.Notes != "人工备注" || len(previous.AllowedGroups) != 1 || previous.AllowedGroups[0] != 3 {
		t.Fatalf("previous account snapshot changed: %#v", previous)
	}
	if adminService.updateInput == nil || adminService.updateInput.Notes == nil || !strings.Contains(*adminService.updateInput.Notes, "人工备注") || !hasExternalID(*adminService.updateInput.Notes, "workmesh:site-12") {
		t.Fatalf("existing notes were not preserved: %#v", adminService.updateInput)
	}
	if adminService.updateInput.AllowedGroups == nil || !equalInt64Sets(*adminService.updateInput.AllowedGroups, []int64{3, 8}) {
		t.Fatalf("allowed groups were not merged: %#v", adminService.updateInput)
	}
}
