package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type dolphinSearchKBService struct {
	interfaces.KnowledgeBaseService
	kbs map[string]*types.KnowledgeBase
}

func (s *dolphinSearchKBService) GetKnowledgeBasesByIDsOnly(
	_ context.Context,
	ids []string,
) ([]*types.KnowledgeBase, error) {
	out := make([]*types.KnowledgeBase, 0, len(ids))
	for _, id := range ids {
		if kb := s.kbs[id]; kb != nil {
			out = append(out, kb)
		}
	}
	return out, nil
}

type dolphinSearchShareService struct {
	interfaces.KBShareService
	// allowed maps kbID -> set of tenant IDs that may view the shared KB.
	allowed map[string]map[uint64]bool
}

func (s *dolphinSearchShareService) HasTenantKBPermission(
	_ context.Context,
	kbID string,
	callerTenantID uint64,
	_ types.TenantRole,
	_ types.OrgMemberRole,
) (bool, error) {
	byTenant, ok := s.allowed[kbID]
	if !ok {
		return false, nil
	}
	return byTenant[callerTenantID], nil
}

type dolphinSearchKnowledgeService struct {
	interfaces.KnowledgeService
	knowledges []*types.Knowledge
}

func (s *dolphinSearchKnowledgeService) GetKnowledgeBatchWithSharedAccess(
	_ context.Context,
	tenantID uint64,
	ids []string,
) ([]*types.Knowledge, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make([]*types.Knowledge, 0)
	for _, k := range s.knowledges {
		if k != nil && wanted[k.ID] && k.TenantID == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestPartitionKBsForTenant(t *testing.T) {
	svc := &sessionService{
		kbShareService: &dolphinSearchShareService{
			allowed: map[string]map[uint64]bool{
				"shared-kb": {100: true},
			},
		},
	}
	kbByID := map[string]*types.KnowledgeBase{
		"own-kb":     {ID: "own-kb", TenantID: 100},
		"shared-kb":  {ID: "shared-kb", TenantID: 300},
		"foreign-kb": {ID: "foreign-kb", TenantID: 999},
	}
	ids, err := svc.partitionKBsForTenant(
		context.Background(),
		[]string{"own-kb", "shared-kb", "foreign-kb", "missing-kb"},
		kbByID,
		types.DolphinSearchTenantScope{TenantID: 100, Role: types.TenantRoleOwner},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"own-kb", "shared-kb"}, ids)
}

func TestPartitionKBsForTenantIgnoresDeleted(t *testing.T) {
	svc := &sessionService{}
	deleted := types.KnowledgeBase{
		ID:       "gone",
		TenantID: 100,
		DeletedAt: gorm.DeletedAt{
			Time:  time.Now(),
			Valid: true,
		},
	}
	ids, err := svc.partitionKBsForTenant(
		context.Background(),
		[]string{"gone"},
		map[string]*types.KnowledgeBase{"gone": &deleted},
		types.DolphinSearchTenantScope{TenantID: 100, Role: types.TenantRoleOwner},
	)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestPartitionKnowledgeForTenant(t *testing.T) {
	svc := &sessionService{
		knowledgeService: &dolphinSearchKnowledgeService{
			knowledges: []*types.Knowledge{
				{ID: "k-1", TenantID: 100},
				{ID: "k-2", TenantID: 200},
				{ID: "k-3", TenantID: 100},
			},
		},
	}
	ids, err := svc.partitionKnowledgeForTenant(
		context.Background(), 100, []string{"k-1", "k-2", "k-3", "k-4"},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"k-1", "k-3"}, ids)
}

func TestPartitionKnowledgeForTenantEmpty(t *testing.T) {
	svc := &sessionService{}
	ids, err := svc.partitionKnowledgeForTenant(context.Background(), 100, nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
}

// TestSearchKnowledgeContextEmptyScopeReturnsEmptyArrays guards the response
// shape when a pinned scope yields no accessible KBs: search_results and
// tenant_results must serialise as [] (not null), matching the no-scope path.
func TestSearchKnowledgeContextEmptyScopeReturnsEmptyArrays(t *testing.T) {
	svc := &sessionService{
		tenantService: &dolphinTenantService{
			tenants: map[uint64]*types.Tenant{
				100: {ID: 100, Name: "ws"},
			},
		},
		knowledgeBaseService: &dolphinSearchKBService{
			kbs: map[string]*types.KnowledgeBase{
				"foreign-kb": {ID: "foreign-kb", TenantID: 999},
			},
		},
		kbShareService: &dolphinSearchShareService{allowed: map[string]map[uint64]bool{}},
	}

	res, err := svc.SearchKnowledgeContext(context.Background(), &types.DolphinSearchRequest{
		UserID:           "u-1",
		Tenants:          []types.DolphinSearchTenantScope{{TenantID: 100, Role: types.TenantRoleOwner}},
		Query:            "hello",
		KnowledgeBaseIDs: []string{"foreign-kb"},
		TopK:             5,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.Total)
	assert.NotNil(t, res.SearchResults, "search_results must be [] not null")
	assert.Empty(t, res.SearchResults)
	assert.NotNil(t, res.TenantResults, "tenant_results must be [] not null")
	assert.Empty(t, res.TenantResults)
}

func TestTenantUserContext(t *testing.T) {
	svc := &sessionService{
		tenantService: &dolphinTenantService{
			tenants: map[uint64]*types.Tenant{
				42: {ID: 42, Name: "answers"},
			},
		},
	}
	ctx, err := svc.tenantUserContext(context.Background(), 42, "user-1", types.TenantRoleAdmin)
	require.NoError(t, err)
	tenantID, ok := types.TenantIDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint64(42), tenantID)
	userID, ok := types.UserIDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "user-1", userID)
	assert.Equal(t, types.TenantRoleAdmin, types.TenantRoleFromContext(ctx))
	tenant, ok := types.TenantInfoFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "answers", tenant.Name)
	// The acting context must also replace the caller captured by the auth
	// middleware (a platform API key principal with TenantID 0), because
	// HybridSearch authorizes through CallerFromContext, which prefers that
	// captured caller over the TenantID/UserID keys.
	caller := types.CallerFromContext(ctx)
	assert.Equal(t, uint64(42), caller.TenantID)
	assert.Equal(t, "user-1", caller.UserID)
	assert.Equal(t, types.TenantRoleAdmin, caller.Role)
}

// TestTenantUserContextOverridesAuthenticatedCaller guards the /dolphin
// "search as user" path: the auth middleware stores the platform API key
// principal (TenantID 0) in the request context, and the acting context must
// override it so the search is authorized as the target user in the target
// workspace instead of as the platform caller.
func TestTenantUserContextOverridesAuthenticatedCaller(t *testing.T) {
	svc := &sessionService{
		tenantService: &dolphinTenantService{
			tenants: map[uint64]*types.Tenant{
				100: {ID: 100, Name: "ws"},
			},
		},
	}
	platformCtx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 0,
		UserID:   "platform-api-key:7",
		Role:     types.TenantRoleViewer,
	})
	ctx, err := svc.tenantUserContext(platformCtx, 100, "target-user", types.TenantRoleOwner)
	require.NoError(t, err)

	caller := types.CallerFromContext(ctx)
	assert.Equal(t, uint64(100), caller.TenantID)
	assert.Equal(t, "target-user", caller.UserID)
	assert.Equal(t, types.TenantRoleOwner, caller.Role)
}

type dolphinTenantService struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
}

func (s *dolphinTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return s.tenants[id], nil
}
