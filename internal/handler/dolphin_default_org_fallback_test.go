package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDolphinUserService is a minimal UserService fixture.
type stubDolphinUserService struct {
	interfaces.UserService
	getByEmail func(ctx context.Context, email string) (*types.User, error)
}

func (s *stubDolphinUserService) GetUserByEmail(ctx context.Context, email string) (*types.User, error) {
	return s.getByEmail(ctx, email)
}

// stubDolphinTenantService is a minimal TenantService fixture.
type stubDolphinTenantService struct {
	interfaces.TenantService
	getByID func(ctx context.Context, tenantID uint64) (*types.Tenant, error)
}

func (s *stubDolphinTenantService) GetTenantByID(ctx context.Context, tenantID uint64) (*types.Tenant, error) {
	return s.getByID(ctx, tenantID)
}

// stubDolphinKBService is a minimal KnowledgeBaseService fixture.
type stubDolphinKBService struct {
	interfaces.KnowledgeBaseService
	listByTenant func(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error)
}

func (s *stubDolphinKBService) ListKnowledgeBasesByTenantID(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) {
	return s.listByTenant(ctx, tenantID)
}

// stubDolphinKBShareService is a minimal KBShareService fixture.
type stubDolphinKBShareService struct {
	interfaces.KBShareService
	listSharedInOrg func(ctx context.Context, orgID string, tenantID uint64, role types.TenantRole) ([]*types.OrganizationSharedKnowledgeBaseItem, error)
}

func (s *stubDolphinKBShareService) ListSharedKnowledgeBasesInOrganization(
	ctx context.Context,
	orgID string,
	tenantID uint64,
	callerTenantRole types.TenantRole,
) ([]*types.OrganizationSharedKnowledgeBaseItem, error) {
	return s.listSharedInOrg(ctx, orgID, tenantID, callerTenantRole)
}

// stubDolphinSessionService is a minimal SessionService fixture.
type stubDolphinSessionService struct {
	interfaces.SessionService
	searchKnowledgeContext func(ctx context.Context, req *types.DolphinSearchRequest) (*types.DolphinSearchResult, error)
}

func (s *stubDolphinSessionService) SearchKnowledgeContext(ctx context.Context, req *types.DolphinSearchRequest) (*types.DolphinSearchResult, error) {
	return s.searchKnowledgeContext(ctx, req)
}

// mustJSONReader encodes v as a JSON request body for httptest.
func mustJSONReader(v interface{}) io.Reader {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return bytes.NewReader(b)
}

func TestOrgMemberRoleToTenantRole(t *testing.T) {
	assert.Equal(t, types.TenantRoleAdmin, orgMemberRoleToTenantRole(types.OrgRoleAdmin))
	assert.Equal(t, types.TenantRoleContributor, orgMemberRoleToTenantRole(types.OrgRoleEditor))
	assert.Equal(t, types.TenantRoleViewer, orgMemberRoleToTenantRole(types.OrgRoleViewer))
	assert.Equal(t, types.TenantRoleViewer, orgMemberRoleToTenantRole("bogus"))
}

func TestDefaultOrgScopes(t *testing.T) {
	t.Run("no default org configured returns nil", func(t *testing.T) {
		h := &DolphinHandler{cfg: &config.Config{}}
		scopes, err := h.defaultOrgScopes(context.Background())
		require.NoError(t, err)
		assert.Nil(t, scopes)
	})

	t.Run("maps org member roles and dedups tenants", func(t *testing.T) {
		h := &DolphinHandler{
			cfg: &config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: "org-1"}},
			orgService: &stubDolphinOrgService{
				listTenantMembers: func(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error) {
					return []*types.OrganizationTenantMember{
						{TenantID: 10008, Role: types.OrgRoleAdmin},
						{TenantID: 20001, Role: types.OrgRoleViewer},
						{TenantID: 10008, Role: types.OrgRoleEditor}, // duplicate
					}, nil
				},
			},
		}
		scopes, err := h.defaultOrgScopes(context.Background())
		require.NoError(t, err)
		require.Len(t, scopes, 2)
		assert.Equal(t, types.TenantRoleAdmin, scopes[0].Role)
		assert.Equal(t, types.TenantRoleViewer, scopes[1].Role)
	})
}

// TestListUserKnowledgeBasesUnknownEmailFallsBackToDefaultOrg verifies that
// an email with no account is resolved against the default organization
// instead of returning 404: KBs created in the org's member workspaces plus
// KBs shared into the org.
func TestListUserKnowledgeBasesUnknownEmailFallsBackToDefaultOrg(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &DolphinHandler{
		cfg: &config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: "org-default"}},
		userService: &stubDolphinUserService{
			getByEmail: func(ctx context.Context, email string) (*types.User, error) { return nil, nil },
		},
		tenantMemberSvc: &stubDolphinMemberService{},
		tenantService: &stubDolphinTenantService{
			getByID: func(ctx context.Context, tenantID uint64) (*types.Tenant, error) {
				return &types.Tenant{ID: tenantID, Name: fmt.Sprintf("workspace-%d", tenantID)}, nil
			},
		},
		kbService: &stubDolphinKBService{
			listByTenant: func(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) {
				return []*types.KnowledgeBase{
					{ID: fmt.Sprintf("own-kb-%d", tenantID), TenantID: tenantID, Name: "own"},
				}, nil
			},
		},
		kbShareService: &stubDolphinKBShareService{
			listSharedInOrg: func(ctx context.Context, orgID string, tenantID uint64, role types.TenantRole) ([]*types.OrganizationSharedKnowledgeBaseItem, error) {
				return []*types.OrganizationSharedKnowledgeBaseItem{
					{SharedKnowledgeBaseInfo: types.SharedKnowledgeBaseInfo{
						KnowledgeBase:  &types.KnowledgeBase{ID: "org-shared-kb", TenantID: 999, Name: "shared"},
						OrganizationID: orgID,
						SourceTenantID: 999,
					}},
				}, nil
			},
		},
		orgService: &stubDolphinOrgService{
			listTenantMembers: func(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error) {
				return []*types.OrganizationTenantMember{
					{TenantID: 10008, Role: types.OrgRoleAdmin},
					{TenantID: 20001, Role: types.OrgRoleViewer},
				}, nil
			},
			getOrganization: func(ctx context.Context, id string) (*types.Organization, error) {
				return &types.Organization{ID: id, Name: "Default Org"}, nil
			},
		},
	}

	router := gin.New()
	router.GET("/dolphin/knowledge-bases", h.ListUserKnowledgeBases)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dolphin/knowledge-bases?email=nobody%40example.com", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data := body["data"].(map[string]interface{})

	user := data["user"].(map[string]interface{})
	assert.Equal(t, "nobody@example.com", user["email"])
	assert.Equal(t, "", user["id"])

	org := data["organization"].(map[string]interface{})
	assert.Equal(t, "org-default", org["id"])
	assert.Equal(t, "Default Org", org["name"])

	kbs := data["knowledge_bases"].([]interface{})
	// Only the KBs shared into the default org are returned — member
	// workspaces' own KBs must NOT leak into the fallback.
	require.Len(t, kbs, 1)
	row := kbs[0].(map[string]interface{})
	assert.Equal(t, "shared", row["source"])
	assert.Equal(t, "org-shared-kb", row["knowledge_base"].(map[string]interface{})["id"])
	assert.Equal(t, float64(1), data["total"])
}

// TestListUserKnowledgeBasesUnknownEmailWithoutDefaultOrgKeeps404 guards the
// original behaviour: without DOLPHIN_DEFAULT_ORGANIZATION an unknown email
// still returns 404.
func TestListUserKnowledgeBasesUnknownEmailWithoutDefaultOrgKeeps404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &DolphinHandler{
		cfg: &config.Config{},
		userService: &stubDolphinUserService{
			getByEmail: func(ctx context.Context, email string) (*types.User, error) { return nil, nil },
		},
	}
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.GET("/dolphin/knowledge-bases", h.ListUserKnowledgeBases)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dolphin/knowledge-bases?email=nobody%40example.com", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestSearchKnowledgeContextUnknownEmailFallsBackToDefaultOrg verifies the
// search fallback: an unknown email searches across the default organization
// using the email as a synthetic attribution identity.
func TestSearchKnowledgeContextUnknownEmailFallsBackToDefaultOrg(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotReq *types.DolphinSearchRequest
	h := &DolphinHandler{
		cfg: &config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: "org-default"}},
		userService: &stubDolphinUserService{
			getByEmail: func(ctx context.Context, email string) (*types.User, error) { return nil, nil },
		},
		tenantMemberSvc: &stubDolphinMemberService{},
		kbService: &stubDolphinKBService{
			listByTenant: func(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) {
				return []*types.KnowledgeBase{
					{ID: fmt.Sprintf("own-kb-%d", tenantID), TenantID: tenantID, Name: "own"},
				}, nil
			},
		},
		kbShareService: &stubDolphinKBShareService{
			listSharedInOrg: func(ctx context.Context, orgID string, tenantID uint64, role types.TenantRole) ([]*types.OrganizationSharedKnowledgeBaseItem, error) {
				return []*types.OrganizationSharedKnowledgeBaseItem{
					{SharedKnowledgeBaseInfo: types.SharedKnowledgeBaseInfo{
						KnowledgeBase:  &types.KnowledgeBase{ID: "org-shared-kb", TenantID: 999, Name: "shared"},
						OrganizationID: orgID,
						SourceTenantID: 999,
					}},
				}, nil
			},
		},
		orgService: &stubDolphinOrgService{
			listTenantMembers: func(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error) {
				return []*types.OrganizationTenantMember{
					{TenantID: 10008, Role: types.OrgRoleAdmin},
				}, nil
			},
		},
		sessionService: &stubDolphinSessionService{
			searchKnowledgeContext: func(ctx context.Context, req *types.DolphinSearchRequest) (*types.DolphinSearchResult, error) {
				gotReq = req
				return &types.DolphinSearchResult{
					Query:         req.Query,
					SearchResults: []*types.SearchResult{{ID: "chunk-1"}},
					Contexts:      "<documents/>",
					Total:         1,
					TenantResults: []types.DolphinSearchTenantResult{{TenantID: 10008, ResultCount: 1}},
				}, nil
			},
		},
	}

	router := gin.New()
	router.POST("/dolphin/search-context", h.SearchKnowledgeContext)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dolphin/search-context",
		mustJSONReader(map[string]interface{}{"email": "nobody@example.com", "query": "how to deploy"}))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, gotReq)
	// Synthetic identity = the requested email; scopes = default org members.
	assert.Equal(t, "nobody@example.com", gotReq.UserID)
	require.Len(t, gotReq.Tenants, 1)
	assert.Equal(t, uint64(10008), gotReq.Tenants[0].TenantID)
	// No pinned scope → fall back to default-org shared KBs only.
	require.Equal(t, []string{"org-shared-kb"}, gotReq.KnowledgeBaseIDs)
	assert.NotContains(t, gotReq.KnowledgeBaseIDs, "own-kb-10008")

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data := body["data"].(map[string]interface{})
	assert.Equal(t, "nobody@example.com", data["user"].(map[string]interface{})["email"])
	assert.Equal(t, "org-default", data["organization"].(map[string]interface{})["id"])
	assert.Equal(t, float64(1), data["total"])
}

// TestSearchKnowledgeContextUnknownEmailUsesSingleRepresentativeScope guards
// against the N×-redundant search: even when the default org has many member
// workspaces, the fallback must search from ONE representative workspace only
// (org-shared KBs are identical for every member), otherwise latency scales
// with the member count.
func TestSearchKnowledgeContextUnknownEmailUsesSingleRepresentativeScope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotReq *types.DolphinSearchRequest
	h := &DolphinHandler{
		cfg: &config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: "org-default"}},
		userService: &stubDolphinUserService{
			getByEmail: func(ctx context.Context, email string) (*types.User, error) { return nil, nil },
		},
		tenantMemberSvc: &stubDolphinMemberService{},
		kbService:       &stubDolphinKBService{listByTenant: func(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) { return nil, nil }},
		kbShareService: &stubDolphinKBShareService{
			listSharedInOrg: func(ctx context.Context, orgID string, tenantID uint64, role types.TenantRole) ([]*types.OrganizationSharedKnowledgeBaseItem, error) {
				return []*types.OrganizationSharedKnowledgeBaseItem{
					{SharedKnowledgeBaseInfo: types.SharedKnowledgeBaseInfo{KnowledgeBase: &types.KnowledgeBase{ID: "org-shared-kb", TenantID: 999, Name: "shared"}}},
				}, nil
			},
		},
		orgService: &stubDolphinOrgService{
			listTenantMembers: func(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error) {
				// 15 members, highest role = admin on tenant 20001.
				members := []*types.OrganizationTenantMember{
					{TenantID: 10008, Role: types.OrgRoleViewer},
					{TenantID: 20001, Role: types.OrgRoleAdmin},
					{TenantID: 30001, Role: types.OrgRoleEditor},
				}
				for i := uint64(40001); i < 40013; i++ {
					members = append(members, &types.OrganizationTenantMember{TenantID: i, Role: types.OrgRoleViewer})
				}
				return members, nil
			},
		},
		sessionService: &stubDolphinSessionService{
			searchKnowledgeContext: func(ctx context.Context, req *types.DolphinSearchRequest) (*types.DolphinSearchResult, error) {
				gotReq = req
				return &types.DolphinSearchResult{Query: req.Query, SearchResults: []*types.SearchResult{}, Total: 0}, nil
			},
		},
	}

	router := gin.New()
	router.POST("/dolphin/search-context", h.SearchKnowledgeContext)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dolphin/search-context",
		mustJSONReader(map[string]interface{}{"email": "nobody@example.com", "query": "how to deploy"}))
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, gotReq)
	// Exactly one scope, and it must be the highest-privileged member.
	require.Len(t, gotReq.Tenants, 1)
	assert.Equal(t, uint64(20001), gotReq.Tenants[0].TenantID)
	assert.Equal(t, types.TenantRoleAdmin, gotReq.Tenants[0].Role)
}
