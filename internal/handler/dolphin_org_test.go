package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDolphinOrgService is a minimal OrganizationService fixture for the
// dolphin default-org auto-join tests.
type stubDolphinOrgService struct {
	interfaces.OrganizationService
	getTenantMember   func(ctx context.Context, orgID string, tenantID uint64) (*types.OrganizationTenantMember, error)
	addTenantMember   func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error
	listTenantMembers func(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error)
	getOrganization   func(ctx context.Context, id string) (*types.Organization, error)
}

func (s *stubDolphinOrgService) GetTenantMember(ctx context.Context, orgID string, tenantID uint64) (*types.OrganizationTenantMember, error) {
	return s.getTenantMember(ctx, orgID, tenantID)
}

func (s *stubDolphinOrgService) AddTenantMember(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
	return s.addTenantMember(ctx, orgID, tenantID, representativeUserID, role)
}

func (s *stubDolphinOrgService) ListTenantMembers(ctx context.Context, orgID string) ([]*types.OrganizationTenantMember, error) {
	if s.listTenantMembers != nil {
		return s.listTenantMembers(ctx, orgID)
	}
	return []*types.OrganizationTenantMember{}, nil
}

func (s *stubDolphinOrgService) GetOrganization(ctx context.Context, id string) (*types.Organization, error) {
	if s.getOrganization != nil {
		return s.getOrganization(ctx, id)
	}
	return nil, nil
}

// stubDolphinMemberService is a TenantMemberService fixture exposing only
// ListByUser (the one call userTenantScopes needs).
type stubDolphinMemberService struct {
	interfaces.TenantMemberService
	listByUser func(ctx context.Context, userID string) ([]*types.TenantMember, error)
}

func (s *stubDolphinMemberService) ListByUser(ctx context.Context, userID string) ([]*types.TenantMember, error) {
	if s.listByUser != nil {
		return s.listByUser(ctx, userID)
	}
	return []*types.TenantMember{}, nil
}

func newDolphinHandlerForOrg(
	cfg *config.Config,
	orgSvc interfaces.OrganizationService,
	memberSvc interfaces.TenantMemberService,
) *DolphinHandler {
	return &DolphinHandler{
		cfg:             cfg,
		orgService:      orgSvc,
		tenantMemberSvc: memberSvc,
	}
}

func TestEnsureDefaultOrganizationMembership(t *testing.T) {
	const (
		defaultOrg = "default-org-uuid"
		userID     = "user-1"
	)
	user := &types.User{ID: userID, Email: "u@example.com", TenantID: 10008}

	memberInOrg := func(_ context.Context, orgID string, tenantID uint64) (*types.OrganizationTenantMember, error) {
		return &types.OrganizationTenantMember{
			OrganizationID: orgID,
			TenantID:       tenantID,
			Role:           types.OrgRoleViewer,
		}, nil
	}
	notInOrg := func(context.Context, string, uint64) (*types.OrganizationTenantMember, error) {
		return nil, service.ErrTenantNotInOrg
	}

	t.Run("no default org configured skips everything", func(t *testing.T) {
		joined := false
		h := newDolphinHandlerForOrg(&config.Config{}, &stubDolphinOrgService{
			getTenantMember: func(ctx context.Context, orgID string, tenantID uint64) (*types.OrganizationTenantMember, error) {
				t.Fatal("GetTenantMember must not be called when no default org is set")
				return nil, nil
			},
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				joined = true
				return nil
			},
		}, &stubDolphinMemberService{})
		h.ensureDefaultOrganizationMembership(context.Background(), user)
		assert.False(t, joined)
	})

	t.Run("already a member is a no-op", func(t *testing.T) {
		joined := false
		h := newDolphinHandlerForOrg(&config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: defaultOrg}}, &stubDolphinOrgService{
			getTenantMember: memberInOrg,
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				joined = true
				return nil
			},
		}, &stubDolphinMemberService{})
		h.ensureDefaultOrganizationMembership(context.Background(), user)
		assert.False(t, joined)
	})

	t.Run("not a member auto-joins home tenant as viewer", func(t *testing.T) {
		var joinedTenant uint64
		var joinedRole types.OrgMemberRole
		var joinedRep string
		h := newDolphinHandlerForOrg(&config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: defaultOrg}}, &stubDolphinOrgService{
			getTenantMember: notInOrg,
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				joinedTenant, joinedRole, joinedRep = tenantID, role, representativeUserID
				return nil
			},
		}, &stubDolphinMemberService{})
		h.ensureDefaultOrganizationMembership(context.Background(), user)
		assert.Equal(t, uint64(10008), joinedTenant)
		assert.Equal(t, types.OrgRoleViewer, joinedRole)
		assert.Equal(t, userID, joinedRep)
	})

	t.Run("member via another workspace skips join", func(t *testing.T) {
		joined := false
		h := newDolphinHandlerForOrg(&config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: defaultOrg}}, &stubDolphinOrgService{
			getTenantMember: func(ctx context.Context, orgID string, tenantID uint64) (*types.OrganizationTenantMember, error) {
				if tenantID == 20001 {
					return memberInOrg(ctx, orgID, tenantID)
				}
				return nil, service.ErrTenantNotInOrg
			},
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				joined = true
				return nil
			},
		}, &stubDolphinMemberService{
			listByUser: func(ctx context.Context, userID string) ([]*types.TenantMember, error) {
				return []*types.TenantMember{
					{TenantID: 10008, Status: types.TenantMemberStatusActive},
					{TenantID: 20001, Status: types.TenantMemberStatusActive},
				}, nil
			},
		})
		h.ensureDefaultOrganizationMembership(context.Background(), user)
		assert.False(t, joined)
	})

	t.Run("join failure only warns and does not error", func(t *testing.T) {
		h := newDolphinHandlerForOrg(&config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: defaultOrg}}, &stubDolphinOrgService{
			getTenantMember: notInOrg,
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				return errors.New("boom")
			},
		}, &stubDolphinMemberService{})
		// Must not panic / return anything; the method has no error return by design.
		require.NotPanics(t, func() {
			h.ensureDefaultOrganizationMembership(context.Background(), user)
		})
	})

	t.Run("user without home tenant skips join", func(t *testing.T) {
		joined := false
		noHomeUser := &types.User{ID: userID, Email: "u@example.com", TenantID: 0}
		h := newDolphinHandlerForOrg(&config.Config{Dolphin: &config.DolphinConfig{DefaultOrganization: defaultOrg}}, &stubDolphinOrgService{
			getTenantMember: notInOrg,
			addTenantMember: func(ctx context.Context, orgID string, tenantID uint64, representativeUserID string, role types.OrgMemberRole) error {
				joined = true
				return nil
			},
		}, &stubDolphinMemberService{})
		h.ensureDefaultOrganizationMembership(context.Background(), noHomeUser)
		assert.False(t, joined)
	})
}
