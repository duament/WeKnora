package handler

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
)

// DolphinHandler hosts cross-workspace admin endpoints mounted under
// /dolphin. These are control-plane style APIs aimed at operators: the
// caller authenticates with a platform (admin) API key created via
// /system/admin/api-keys, identifies a target user by email, and reads
// data on that user's behalf across every workspace they belong to.
//
// Because the endpoints are not bound to the caller's own workspace,
// they deliberately do NOT read tenant/user from the auth context: the
// target user is resolved from the request parameter, and the caller's
// only authority is the platform API-key capability policy registered
// in the router (system_tenants_read / system_tenants_manage).
type DolphinHandler struct {
	cfg             *config.Config
	userService     interfaces.UserService
	tenantMemberSvc interfaces.TenantMemberService
	tenantService   interfaces.TenantService
	kbService       interfaces.KnowledgeBaseService
	kbShareService  interfaces.KBShareService
	sessionService  interfaces.SessionService
	orgService      interfaces.OrganizationService
}

// NewDolphinHandler creates a new dolphin handler.
func NewDolphinHandler(
	cfg *config.Config,
	userService interfaces.UserService,
	tenantMemberSvc interfaces.TenantMemberService,
	tenantService interfaces.TenantService,
	kbService interfaces.KnowledgeBaseService,
	kbShareService interfaces.KBShareService,
	sessionService interfaces.SessionService,
	orgService interfaces.OrganizationService,
) *DolphinHandler {
	return &DolphinHandler{
		cfg:             cfg,
		userService:     userService,
		tenantMemberSvc: tenantMemberSvc,
		tenantService:   tenantService,
		kbService:       kbService,
		kbShareService:  kbShareService,
		sessionService:  sessionService,
		orgService:      orgService,
	}
}

// resolveUserByEmail looks up a user by email, tolerating case
// differences in the input. Returns (nil, nil) when no user matches.
func (h *DolphinHandler) resolveUserByEmail(ctx context.Context, email string) (*types.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	user, err := h.userService.GetUserByEmail(ctx, email)
	if err == nil && user != nil {
		return user, nil
	}
	if err != nil && !stderrors.Is(err, repository.ErrUserNotFound) {
		return nil, err
	}
	// Exact match missed; retry with a lowercased form. Emails are
	// conventionally case-insensitive even though registration stores
	// whatever the client sent.
	lower := strings.ToLower(email)
	if lower == email {
		return nil, nil
	}
	user, err = h.userService.GetUserByEmail(ctx, lower)
	if err != nil {
		if stderrors.Is(err, repository.ErrUserNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}

// ListUserKnowledgeBases godoc
// @Summary      获取指定用户的所有知识库（含共享）
// @Description  使用平台 admin API key 鉴权，通过用户 email 查询该用户在所有工作区下拥有的知识库，
//
//	以及共享给该用户所在工作区的知识库。跨工作区控制面接口，不依赖调用者自身的
//	X-Tenant-ID / JWT 上下文。
//
// @Tags         Dolphin
// @Produce      json
// @Param        email query string false "用户 email（与 path 二选一）"
// @Param        email path string false "用户 email（与 query 二选一）"
// @Success      200 {object} map[string]interface{} "用户及其知识库列表"
// @Failure      400 {object} apperrors.AppError "缺少 email 参数"
// @Failure      404 {object} apperrors.AppError "用户不存在"
// @Failure      500 {object} apperrors.AppError "服务器错误"
// @Security     ApiKeyAuth
// @Router       /dolphin/knowledge-bases [get]
func (h *DolphinHandler) ListUserKnowledgeBases(c *gin.Context) {
	ctx := c.Request.Context()

	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		email = strings.TrimSpace(c.Param("email"))
	}
	if email == "" {
		c.Error(apperrors.NewBadRequestError("email is required (query or path)"))
		return
	}

	user, err := h.resolveUserByEmail(ctx, email)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{
			"email": sanitizeForLog(email),
		})
		c.Error(apperrors.NewInternalServerError("failed to look up user by email"))
		return
	}
	if user == nil {
		// Unknown email: fall back to the default organization (when
		// configured) instead of failing with 404.
		h.serveDefaultOrgKnowledgeBases(c, ctx, email)
		return
	}

	// Auto-join the target user into the configured default organization
	// (best-effort, never blocks the request) before listing knowledge bases.
	h.ensureDefaultOrganizationMembership(ctx, user)

	tenantScopes, err := h.userTenantScopes(ctx, user)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{"user_id": user.ID})
		c.Error(apperrors.NewInternalServerError("failed to list user memberships"))
		return
	}

	entries := h.listKnowledgeBasesForScopes(ctx, tenantScopes, &user.ID, func(ctx context.Context, scope types.DolphinSearchTenantScope) ([]*types.SharedKnowledgeBaseInfo, error) {
		return h.kbShareService.ListSharedKnowledgeBases(ctx, scope.TenantID, scope.Role)
	})

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user": gin.H{
				"id":       user.ID,
				"email":    user.Email,
				"username": user.Username,
			},
			"knowledge_bases": entries,
			"total":           len(entries),
		},
	})
}

// dolphinSearchContextRequest is the JSON body of POST /dolphin/search-context.
type dolphinSearchContextRequest struct {
	Email            string   `json:"email"`
	Query            string   `json:"query"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
	KnowledgeIDs     []string `json:"knowledge_ids"`
	TopK             int      `json:"top_k"`
	RerankModelID    string   `json:"rerank_model_id"`
	VectorThreshold  float64  `json:"vector_threshold"`
	KeywordThreshold float64  `json:"keyword_threshold"`
	RerankThreshold  float64  `json:"rerank_threshold"`
}

// SearchKnowledgeContext godoc
// @Summary      以指定用户身份搜索知识库并返回处理完成的上下文
// @Description  调用方已经理解用户意图后调用本接口：使用平台 admin API key 鉴权，
//
//	通过 email 定位目标用户，以该用户身份在其所属各工作区内按 chat_pipeline
//	（search_parallel：并行检索 → 重排 → 合并 → top K）方式搜索知识库，并将结果
//	处理成可直接注入提示词的 RAG 上下文返回。不进行查询改写或 LLM 总结。
//
// @Tags         Dolphin
// @Accept       json
// @Produce      json
// @Param        email   path      string                      false "用户 email（与 body 二选一）"
// @Param        request body      dolphinSearchContextRequest true  "搜索请求"
// @Success      200     {object}  map[string]interface{}      "搜索结果与处理完成的上下文"
// @Failure      400     {object}  apperrors.AppError             "请求参数错误"
// @Failure      404     {object}  apperrors.AppError             "用户不存在"
// @Failure      500     {object}  apperrors.AppError             "服务器错误"
// @Security     ApiKeyAuth
// @Router       /dolphin/search-context [post]
func (h *DolphinHandler) SearchKnowledgeContext(c *gin.Context) {
	ctx := c.Request.Context()

	var request dolphinSearchContextRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}

	email := strings.TrimSpace(request.Email)
	if email == "" {
		email = strings.TrimSpace(c.Param("email"))
	}
	if email == "" {
		c.Error(apperrors.NewBadRequestError("email is required (body or path)"))
		return
	}
	if strings.TrimSpace(request.Query) == "" {
		c.Error(apperrors.NewBadRequestError("query cannot be empty"))
		return
	}

	user, err := h.resolveUserByEmail(ctx, email)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{
			"email": sanitizeForLog(email),
		})
		c.Error(apperrors.NewInternalServerError("failed to look up user by email"))
		return
	}
	if user == nil {
		// Unknown email: fall back to the default organization (when
		// configured) instead of failing with 404.
		h.searchDefaultOrgContext(c, ctx, request, email)
		return
	}

	// Auto-join the target user into the configured default organization
	// (best-effort, never blocks the request) before searching.
	h.ensureDefaultOrganizationMembership(ctx, user)

	scopes, err := h.userTenantScopes(ctx, user)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{"user_id": user.ID})
		c.Error(apperrors.NewInternalServerError("failed to list user memberships"))
		return
	}

	// Normalise the scope the same way the search pipeline does: drop
	// empty entries so a caller passing knowledge_base_ids:[""] (or a
	// list that is entirely empty strings) is treated as "no scope" and
	// falls back to searching every KB the user can access — mirroring
	// the behaviour of an empty array instead of erroring.
	knowledgeBaseIDs := nonEmptyStrings(request.KnowledgeBaseIDs)
	knowledgeIDs := nonEmptyStrings(request.KnowledgeIDs)
	if len(knowledgeBaseIDs) == 0 && len(knowledgeIDs) == 0 {
		knowledgeBaseIDs = h.accessibleKnowledgeBaseIDs(ctx, user, scopes)
		if len(knowledgeBaseIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data": gin.H{
					"user": gin.H{
						"id":       user.ID,
						"email":    user.Email,
						"username": user.Username,
					},
					"query":          strings.TrimSpace(request.Query),
					"contexts":       "",
					"search_results": []*types.SearchResult{},
					"tenant_results": []types.DolphinSearchTenantResult{},
					"total":          0,
				},
			})
			return
		}
	}

	searchReq := &types.DolphinSearchRequest{
		UserID:           user.ID,
		Tenants:          scopes,
		Query:            strings.TrimSpace(request.Query),
		KnowledgeBaseIDs: knowledgeBaseIDs,
		KnowledgeIDs:     knowledgeIDs,
		TopK:             request.TopK,
		RerankModelID:    request.RerankModelID,
		VectorThreshold:  request.VectorThreshold,
		KeywordThreshold: request.KeywordThreshold,
		RerankThreshold:  request.RerankThreshold,
	}

	// Run retrieval as the target user: attach the user object so downstream
	// logging/attribution sees the impersonated identity.
	searchCtx := context.WithValue(ctx, types.UserContextKey, user)
	result, err := h.sessionService.SearchKnowledgeContext(searchCtx, searchReq)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{
			"user_id": user.ID,
			"query":   secutils.SanitizeForLog(searchReq.Query),
		})
		// Preserve the service error's own status (e.g. 400 for a malformed
		// scope) instead of masking every failure as a 500.
		if appErr, ok := apperrors.IsAppError(err); ok {
			c.Error(appErr)
			return
		}
		c.Error(apperrors.NewInternalServerError("failed to search knowledge bases"))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user": gin.H{
				"id":       user.ID,
				"email":    user.Email,
				"username": user.Username,
			},
			"query":          result.Query,
			"contexts":       result.Contexts,
			"search_results": result.SearchResults,
			"tenant_results": result.TenantResults,
			"total":          result.Total,
		},
	})
}

// accessibleKnowledgeBaseIDs returns every knowledge base the target user can
// search: the ones they created plus org-shared KBs in each workspace they
// belong to. Used when a search request does not pin a concrete KB/file scope.
func (h *DolphinHandler) accessibleKnowledgeBaseIDs(
	ctx context.Context,
	user *types.User,
	scopes []types.DolphinSearchTenantScope,
) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, scope := range scopes {
		kbs, err := h.kbService.ListKnowledgeBasesByTenantID(ctx, scope.TenantID)
		if err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"user_id":   user.ID,
				"tenant_id": scope.TenantID,
			}, "[dolphin] list own KBs failed; skipping tenant")
		} else {
			for _, kb := range kbs {
				if kb == nil || kb.DeletedAt.Valid || kb.CreatorID != user.ID || seen[kb.ID] {
					continue
				}
				seen[kb.ID] = true
				ids = append(ids, kb.ID)
			}
		}
		if h.kbShareService == nil {
			continue
		}
		shared, err := h.kbShareService.ListSharedKnowledgeBases(ctx, scope.TenantID, scope.Role)
		if err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"user_id":   user.ID,
				"tenant_id": scope.TenantID,
				"error":     err.Error(),
			}, "[dolphin] list shared KBs failed; skipping tenant")
			continue
		}
		for _, info := range shared {
			if info == nil || info.KnowledgeBase == nil || seen[info.KnowledgeBase.ID] {
				continue
			}
			seen[info.KnowledgeBase.ID] = true
			ids = append(ids, info.KnowledgeBase.ID)
		}
	}
	return ids
}

// sanitizeForLog trims and lowercases an email for safe logging.
func sanitizeForLog(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// nonEmptyStrings returns the input slice with empty entries removed and
// duplicates deduplicated, preserving order. Mirrors the search pipeline's
// scope normalisation so handler-side defaulting and service-side
// validation agree on what counts as "no scope".
func nonEmptyStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// defaultOrganizationID returns the UUID of the default organization
// configured via the DOLPHIN_DEFAULT_ORGANIZATION env var, or "" when unset.
func (h *DolphinHandler) defaultOrganizationID() string {
	if h.cfg != nil && h.cfg.Dolphin != nil {
		return strings.TrimSpace(h.cfg.Dolphin.DefaultOrganization)
	}
	return ""
}

// orgMemberRoleToTenantRole maps an organization member role onto the closest
// tenant-RBAC role so the shared-KB 3-D permission cap and the response
// "permission" labels stay meaningful when the Dolphin APIs act on behalf of
// an org member workspace (the unknown-email fallback path).
func orgMemberRoleToTenantRole(r types.OrgMemberRole) types.TenantRole {
	switch r {
	case types.OrgRoleAdmin:
		return types.TenantRoleAdmin
	case types.OrgRoleEditor:
		return types.TenantRoleContributor
	default:
		return types.TenantRoleViewer
	}
}

// defaultOrgScopes resolves the member workspaces of the configured default
// organization into search scopes. Each scope carries the tenant's role inside
// the org (mapped onto the tenant-RBAC scale) so permission checks behave the
// same way they do for a real member. Returns (nil, nil) when no default org
// is configured.
func (h *DolphinHandler) defaultOrgScopes(ctx context.Context) ([]types.DolphinSearchTenantScope, error) {
	orgID := h.defaultOrganizationID()
	if orgID == "" || h.orgService == nil {
		return nil, nil
	}
	members, err := h.orgService.ListTenantMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	scopes := make([]types.DolphinSearchTenantScope, 0, len(members))
	seen := make(map[uint64]bool)
	for _, m := range members {
		if m == nil || m.TenantID == 0 || seen[m.TenantID] {
			continue
		}
		seen[m.TenantID] = true
		scopes = append(scopes, types.DolphinSearchTenantScope{
			TenantID: m.TenantID,
			Role:     orgMemberRoleToTenantRole(m.Role),
		})
	}
	return scopes, nil
}

// listKnowledgeBasesForScopes collects knowledge-base rows across the given
// workspace scopes: KBs created inside each workspace (optionally restricted
// to a creator, nil = all) plus org-shared KBs returned by sharedFn (nil
// skips the shared section). Failures are logged and skipped per workspace so
// one broken tenant cannot erase the others.
func (h *DolphinHandler) listKnowledgeBasesForScopes(
	ctx context.Context,
	scopes []types.DolphinSearchTenantScope,
	creatorFilter *string,
	sharedFn func(ctx context.Context, scope types.DolphinSearchTenantScope) ([]*types.SharedKnowledgeBaseInfo, error),
) []gin.H {
	entries := make([]gin.H, 0, 32)
	seenKB := make(map[string]bool)

	for _, scope := range scopes {
		tenantID := scope.TenantID
		tenantName := ""
		if t, terr := h.tenantService.GetTenantByID(ctx, tenantID); terr == nil && t != nil {
			tenantName = t.Name
		}

		// 1) Knowledge bases created in this workspace.
		kbs, kerr := h.kbService.ListKnowledgeBasesByTenantID(ctx, tenantID)
		if kerr != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"tenant_id": tenantID,
			}, "[dolphin] list own KBs failed; skipping tenant")
			kbs = nil
		}
		own := make([]*types.KnowledgeBase, 0, len(kbs))
		for _, kb := range kbs {
			if kb == nil || kb.DeletedAt.Valid || seenKB[kb.ID] {
				continue
			}
			if creatorFilter != nil && kb.CreatorID != *creatorFilter {
				continue
			}
			seenKB[kb.ID] = true
			own = append(own, kb)
		}
		enrichKBCreatorNames(ctx, h.userService, own)
		for _, kb := range own {
			entries = append(entries, gin.H{
				"source":         "own",
				"tenant_id":      tenantID,
				"tenant_name":    tenantName,
				"permission":     string(scope.Role),
				"knowledge_base": buildKBResponse(kb, types.DefaultStoreDisplay(), nil),
			})
		}

		// 2) Knowledge bases shared into this workspace (org-shared,
		//    cross-tenant). The sharedFn already applies the 3-D permission
		//    cap using the scope's role.
		if h.kbShareService == nil || sharedFn == nil {
			continue
		}
		shared, serr := sharedFn(ctx, scope)
		if serr != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"tenant_id": tenantID,
				"error":     serr.Error(),
			}, "[dolphin] list shared KBs failed; skipping tenant")
			continue
		}
		for _, info := range shared {
			if info == nil || info.KnowledgeBase == nil {
				continue
			}
			if seenKB[info.KnowledgeBase.ID] {
				continue
			}
			seenKB[info.KnowledgeBase.ID] = true
			row := sharedKBRow(info, nil)
			row["source"] = "shared"
			row["tenant_id"] = tenantID
			row["tenant_name"] = tenantName
			entries = append(entries, row)
		}
	}
	return entries
}

// serveDefaultOrgKnowledgeBases is the fallback for ListUserKnowledgeBases
// when the target email has no account: it returns only the knowledge bases
// shared into the configured default organization (never the member
// workspaces' own KBs). When no default org is configured the original 404
// is preserved.
func (h *DolphinHandler) serveDefaultOrgKnowledgeBases(c *gin.Context, ctx context.Context, email string) {
	orgID := h.defaultOrganizationID()
	if orgID == "" || h.orgService == nil {
		c.Error(apperrors.NewNotFoundError("user not found for email"))
		return
	}

	scopes, err := h.defaultOrgScopes(ctx)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{"org_id": orgID})
		c.Error(apperrors.NewInternalServerError("failed to resolve default organization workspaces"))
		return
	}

	// The fallback returns only the knowledge bases shared into the default
	// organization — never the member workspaces' own KBs. List each member
	// workspace and dedup (the org share set is the same for every member;
	// the first member encountered decides the effective permission shown).
	entries := make([]gin.H, 0, 32)
	seenKB := make(map[string]bool)
	if h.kbShareService != nil {
		for _, scope := range scopes {
			tenantName := ""
			if t, terr := h.tenantService.GetTenantByID(ctx, scope.TenantID); terr == nil && t != nil {
				tenantName = t.Name
			}
			items, serr := h.kbShareService.ListSharedKnowledgeBasesInOrganization(ctx, orgID, scope.TenantID, scope.Role)
			if serr != nil {
				logger.WarnWithFields(ctx, logger.Fields{
					"tenant_id": scope.TenantID,
					"org_id":    orgID,
					"error":     serr.Error(),
				}, "[dolphin] list org-shared KBs failed; skipping workspace")
				continue
			}
			for _, it := range items {
				if it == nil || it.KnowledgeBase == nil || seenKB[it.KnowledgeBase.ID] {
					continue
				}
				seenKB[it.KnowledgeBase.ID] = true
				info := it.SharedKnowledgeBaseInfo
				row := sharedKBRow(&info, nil)
				row["source"] = "shared"
				row["tenant_id"] = scope.TenantID
				row["tenant_name"] = tenantName
				entries = append(entries, row)
			}
		}
	}

	orgName := ""
	if org, oerr := h.orgService.GetOrganization(ctx, orgID); oerr == nil && org != nil {
		orgName = org.Name
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user": gin.H{
				"id":       "",
				"email":    email,
				"username": "",
			},
			"organization": gin.H{
				"id":   orgID,
				"name": orgName,
			},
			"knowledge_bases": entries,
			"total":           len(entries),
		},
	})
}

// searchDefaultOrgContext is the fallback for SearchKnowledgeContext when the
// target email has no account: it searches the knowledge bases shared into the
// configured default organization. The caller-provided email is used as a
// synthetic attribution identity so the retrieval pipeline still runs with a
// non-empty user context. When no default org is configured the original 404
// is preserved.
func (h *DolphinHandler) searchDefaultOrgContext(
	c *gin.Context,
	ctx context.Context,
	request dolphinSearchContextRequest,
	email string,
) {
	orgID := h.defaultOrganizationID()
	if orgID == "" || h.orgService == nil || h.sessionService == nil {
		c.Error(apperrors.NewNotFoundError("user not found for email"))
		return
	}

	query := strings.TrimSpace(request.Query)

	scopes, err := h.defaultOrgScopes(ctx)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{"org_id": orgID})
		c.Error(apperrors.NewInternalServerError("failed to resolve default organization workspaces"))
		return
	}

	empty := func() {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data": gin.H{
				"user": gin.H{
					"id":       "",
					"email":    email,
					"username": "",
				},
				"organization": gin.H{
					"id": orgID,
				},
				"query":          query,
				"contexts":       "",
				"search_results": []*types.SearchResult{},
				"tenant_results": []types.DolphinSearchTenantResult{},
				"total":          0,
			},
		})
	}
	if len(scopes) == 0 {
		empty()
		return
	}

	// Normalise the scope the same way the real-user path does: drop empty
	// entries so knowledge_base_ids:[""] is treated as "no scope".
	knowledgeBaseIDs := nonEmptyStrings(request.KnowledgeBaseIDs)
	knowledgeIDs := nonEmptyStrings(request.KnowledgeIDs)
	if len(knowledgeBaseIDs) == 0 && len(knowledgeIDs) == 0 {
		knowledgeBaseIDs = h.defaultOrgAccessibleKnowledgeBaseIDs(ctx, scopes)
		if len(knowledgeBaseIDs) == 0 {
			empty()
			return
		}
	}

	// The fallback only searches the default org's shared KBs, which are the
	// same set for every member workspace. Passing all member scopes to the
	// pipeline would re-run the identical retrieval once per member (the
	// scoped case above took 15× on 15 members, and the no-scope case 45×),
	// multiplying latency by the member count. A single representative
	// member workspace is enough to reach every org-shared KB; prefer the
	// highest-privileged member so no permission cap can shrink the results.
	searchScopes := scopes[:1]
	if len(scopes) > 1 {
		best := scopes[0]
		for _, s := range scopes[1:] {
			if s.Role.Level() > best.Role.Level() {
				best = s
			}
		}
		searchScopes = []types.DolphinSearchTenantScope{best}
	}

	searchReq := &types.DolphinSearchRequest{
		// The email may not map to a real account; use it as a synthetic
		// identity so the pipeline still runs with a non-empty user context.
		UserID:           email,
		Tenants:          searchScopes,
		Query:            query,
		KnowledgeBaseIDs: knowledgeBaseIDs,
		KnowledgeIDs:     knowledgeIDs,
		TopK:             request.TopK,
		RerankModelID:    request.RerankModelID,
		VectorThreshold:  request.VectorThreshold,
		KeywordThreshold: request.KeywordThreshold,
		RerankThreshold:  request.RerankThreshold,
	}

	// Attach a lightweight synthetic user so downstream logging/attribution
	// sees the requested identity instead of a blank principal.
	searchCtx := context.WithValue(ctx, types.UserContextKey, &types.User{ID: email, Email: email})
	result, err := h.sessionService.SearchKnowledgeContext(searchCtx, searchReq)
	if err != nil {
		logger.ErrorWithFields(ctx, err, logger.Fields{
			"email": sanitizeForLog(email),
			"query": secutils.SanitizeForLog(query),
		})
		// Preserve the service error's own status instead of masking every
		// failure as a 500.
		if appErr, ok := apperrors.IsAppError(err); ok {
			c.Error(appErr)
			return
		}
		c.Error(apperrors.NewInternalServerError("failed to search knowledge bases"))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"user": gin.H{
				"id":       "",
				"email":    email,
				"username": "",
			},
			"organization": gin.H{
				"id": orgID,
			},
			"query":          result.Query,
			"contexts":       result.Contexts,
			"search_results": result.SearchResults,
			"tenant_results": result.TenantResults,
			"total":          result.Total,
		},
	})
}

// defaultOrgAccessibleKnowledgeBaseIDs returns the knowledge bases shared into
// the default organization. Member workspaces' own KBs are intentionally NOT
// included — the fallback only ever surfaces org-shared knowledge. Used when a
// fallback search does not pin a concrete KB/file scope.
func (h *DolphinHandler) defaultOrgAccessibleKnowledgeBaseIDs(
	ctx context.Context,
	scopes []types.DolphinSearchTenantScope,
) []string {
	orgID := h.defaultOrganizationID()
	if orgID == "" || h.kbShareService == nil {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, scope := range scopes {
		items, err := h.kbShareService.ListSharedKnowledgeBasesInOrganization(ctx, orgID, scope.TenantID, scope.Role)
		if err != nil {
			logger.WarnWithFields(ctx, logger.Fields{
				"tenant_id": scope.TenantID,
				"org_id":    orgID,
				"error":     err.Error(),
			}, "[dolphin] list org-shared KBs failed; skipping workspace")
			continue
		}
		for _, it := range items {
			if it == nil || it.KnowledgeBase == nil || seen[it.KnowledgeBase.ID] {
				continue
			}
			seen[it.KnowledgeBase.ID] = true
			ids = append(ids, it.KnowledgeBase.ID)
		}
	}
	return ids
}

// ensureDefaultOrganizationMembership makes sure the target user is a member
// of the default organization configured via the DOLPHIN_DEFAULT_ORGANIZATION
// env var before the actual Dolphin API runs.
//
// Organization membership is per-tenant (Plan 3 of #1303): a "user has joined
// an org" means one of the user's workspaces is a member. If none of the
// target user's workspaces is already in the default org, the user's home
// workspace (user.TenantID) is added automatically as a viewer.
//
// The whole flow is best-effort: a lookup/join failure is only logged as a
// warning and never affects the API result, so a misconfigured org UUID or a
// transient DB error cannot break the control-plane endpoints.
func (h *DolphinHandler) ensureDefaultOrganizationMembership(ctx context.Context, user *types.User) {
	orgID := h.defaultOrganizationID()
	if orgID == "" || h.orgService == nil || user == nil {
		return
	}

	scopes, err := h.userTenantScopes(ctx, user)
	if err != nil {
		logger.WarnWithFields(ctx, logger.Fields{
			"user_id": user.ID,
			"org_id":  orgID,
			"error":   err.Error(),
		}, "[dolphin] failed to list user tenants; skipping default-org auto-join")
		return
	}
	for _, scope := range scopes {
		if _, err := h.orgService.GetTenantMember(ctx, orgID, scope.TenantID); err == nil {
			// The user already belongs to the default org via this workspace.
			return
		} else if !stderrors.Is(err, service.ErrTenantNotInOrg) {
			logger.WarnWithFields(ctx, logger.Fields{
				"user_id":   user.ID,
				"org_id":    orgID,
				"tenant_id": scope.TenantID,
				"error":     err.Error(),
			}, "[dolphin] failed to check default-org membership; skipping auto-join")
			return
		}
	}

	if user.TenantID == 0 {
		logger.WarnWithFields(ctx, logger.Fields{
			"user_id": user.ID,
			"org_id":  orgID,
		}, "[dolphin] target user has no home workspace; cannot auto-join default organization")
		return
	}

	if err := h.orgService.AddTenantMember(ctx, orgID, user.TenantID, user.ID, types.OrgRoleViewer); err != nil {
		logger.WarnWithFields(ctx, logger.Fields{
			"user_id":   user.ID,
			"org_id":    orgID,
			"tenant_id": user.TenantID,
			"error":     err.Error(),
		}, "[dolphin] failed to auto-join target user into default organization")
		return
	}
	logger.Infof(ctx, "[dolphin] auto-joined user %s (tenant %d) into default organization %s",
		user.ID, user.TenantID, orgID)
}

// userTenantScopes collects the workspaces the target user belongs to: every
// active membership, plus the home tenant as a legacy fallback for rows
// created before memberships were backfilled.
func (h *DolphinHandler) userTenantScopes(
	ctx context.Context,
	user *types.User,
) ([]types.DolphinSearchTenantScope, error) {
	scopes := make([]types.DolphinSearchTenantScope, 0, 4)
	seen := make(map[uint64]bool)

	memberships, err := h.tenantMemberSvc.ListByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	for _, m := range memberships {
		if m == nil || m.TenantID == 0 || m.Status != types.TenantMemberStatusActive {
			continue
		}
		if seen[m.TenantID] {
			continue
		}
		seen[m.TenantID] = true
		scopes = append(scopes, types.DolphinSearchTenantScope{TenantID: m.TenantID, Role: m.Role})
	}
	if user.TenantID > 0 && !seen[user.TenantID] {
		scopes = append(scopes, types.DolphinSearchTenantScope{
			TenantID: user.TenantID,
			Role:     types.TenantRoleOwner,
		})
	}
	return scopes, nil
}
