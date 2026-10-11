//go:build unit

package admin

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type exclusiveGroupStub struct{ *stubAdminService }

func (s *exclusiveGroupStub) ListGroups(ctx context.Context, page, size int, platform, status, search string, exclusive *bool, sortBy, sortOrder string) ([]service.Group, int64, error) {
	s.lastListGroupsIsExclusive = exclusive
	var groups []service.Group
	for _, group := range s.groups {
		if exclusive == nil || group.IsExclusive == *exclusive {
			groups = append(groups, group)
		}
	}
	return groups, int64(len(groups)), nil
}

func TestIntegrationGroupListsEnforceExclusiveWithoutChangingAdminLists(t *testing.T) {
	admin := &exclusiveGroupStub{newStubAdminService()}
	admin.groups = []service.Group{{ID: 1, Name: "public", Status: service.StatusActive}, {ID: 2, Name: "exclusive", IsExclusive: true, Status: service.StatusActive}}
	h := &GroupHandler{adminService: admin}
	for _, integration := range []bool{true, false} {
		r := gin.New()
		if integration {
			r.Use(markIntegration)
		}
		r.GET("/groups", h.List)
		r.GET("/groups/all", h.GetAll)
		list := integrationRequest(r, "GET", "/groups?is_exclusive=false", "")
		require.Equal(t, 200, list.Code, list.Body.String())
		require.NotNil(t, admin.lastListGroupsIsExclusive)
		require.Equal(t, integration, *admin.lastListGroupsIsExclusive)
		if integration {
			require.NotContains(t, list.Body.String(), `"name":"public"`)
			require.Contains(t, list.Body.String(), `"total":1`)
		} else {
			require.Contains(t, list.Body.String(), `"name":"public"`)
		}
		all := integrationRequest(r, "GET", "/groups/all", "")
		require.Equal(t, 200, all.Code)
		if integration {
			require.NotContains(t, all.Body.String(), `"name":"public"`)
		} else {
			require.Contains(t, all.Body.String(), `"name":"public"`)
		}
	}
}

func TestIntegrationModelsExcludePublicAndInactiveGroups(t *testing.T) {
	admin := newStubAdminService()
	admin.groups = []service.Group{{ID: 1, Status: service.StatusActive}, {ID: 2, IsExclusive: true, Status: service.StatusActive}, {ID: 3, IsExclusive: true, Status: service.StatusDisabled}}
	models := &integrationModelsStub{}
	keys := &integrationKeyStub{provisioningAPIKeyServiceStub: provisioningAPIKeyServiceStub{groups: admin.groups}, key: &service.APIKey{ID: 9, UserID: 1, RoutingMode: "smart", SmartGroupIDs: []int64{1, 2, 3}}}
	r := integrationRouter(&UserHandler{adminService: admin, integrationKeys: keys, integrationModels: models}, markIntegration)
	for _, path := range []string{"/groups/models", "/users/1/models", "/users/1/models?api_key_id=9"} {
		models.calls = nil
		resp := integrationRequest(r, "GET", path, "")
		require.Equal(t, 200, resp.Code, resp.Body.String())
		require.Equal(t, []int64{2}, models.calls)
		require.Contains(t, resp.Body.String(), `"group_ids":[2]`)
	}
	for _, ids := range []string{"1", "3", "1,2"} {
		resp := integrationRequest(r, "GET", "/groups/models?group_ids="+ids, "")
		require.Equal(t, 404, resp.Code, resp.Body.String())
	}
	keys.key.SmartGroupIDs = []int64{1}
	resp := integrationRequest(r, "GET", "/users/1/models?api_key_id=9", "")
	require.Equal(t, 200, resp.Code)
	require.Contains(t, resp.Body.String(), `"models":[]`)
}

type integrationUsageRepoStub struct{ service.UsageLogRepository }

func (s *integrationUsageRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	private := "private-upstream"
	cost := 50.0
	return []service.UsageLog{{ID: 1, AccountID: 123, RequestedModel: "public-model", Model: private, PointsCost: 12.5, PointsPerUSD: 100,
		BillingType: service.BillingTypePoints, UpstreamEndpoint: &private, UpstreamModel: &private, AccountStatsCost: &cost,
		Account: &service.Account{Name: private}}}, &pagination.PaginationResult{Total: 1}, nil
}
func (s *integrationUsageRepoStub) GetStatsWithFilters(context.Context, usagestats.UsageLogFilters) (*usagestats.UsageStats, error) {
	cost := 50.0
	return &usagestats.UsageStats{TotalRequests: 1, TotalPointsCost: 12.5, TotalAccountCost: &cost,
		UpstreamEndpoints: []usagestats.EndpointStat{{Endpoint: "private-upstream"}}, EndpointPaths: []usagestats.EndpointStat{{Endpoint: "private-upstream"}}}, nil
}

func TestIntegrationUsageResponsesHideUpstreamAndIncludePoints(t *testing.T) {
	repo := &integrationUsageRepoStub{}
	usage := service.NewUsageService(repo, nil, nil, nil)
	h := &UsageHandler{usageService: usage}
	user := &UserHandler{adminService: newStubAdminService(), integrationUsage: usage}
	r := gin.New()
	r.Use(markIntegration)
	r.GET("/usage", h.List)
	r.GET("/usage/stats", h.Stats)
	r.GET("/users/:id/usage", user.GetUserUsage)
	for _, path := range []string{"/usage", "/usage/stats?nocache=true", "/usage/stats?user_id=98765", "/users/1/usage"} {
		resp := integrationRequest(r, "GET", path, "")
		require.Equal(t, 200, resp.Code, resp.Body.String())
		require.Contains(t, resp.Body.String(), `points_cost":12.5`)
		for _, forbidden := range []string{"upstream", "account_cost", "account_id", "account\"", "channel", "mapping"} {
			require.NotContains(t, resp.Body.String(), forbidden)
		}
	}
}
