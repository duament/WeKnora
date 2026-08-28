package service

import (
	"context"
	"fmt"
	"strings"

	chatpipeline "github.com/Tencent/WeKnora/internal/application/service/chat_pipeline"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// SearchKnowledgeContext performs knowledge retrieval as the target user
// across every workspace listed in the request, using the same service path
// as the public knowledge-search API (SessionService.SearchKnowledge:
// search → rerank → merge → filter top K), and returns the merged,
// deduplicated, top-K results together with the rendered RAG context block
// ready for prompt injection.
//
// The caller is expected to have already understood the user's intent; this
// method only executes the retrieval and context processing, it never calls
// an LLM for query understanding or answer generation.
func (s *sessionService) SearchKnowledgeContext(
	ctx context.Context,
	req *types.DolphinSearchRequest,
) (*types.DolphinSearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, apperrors.NewBadRequestError("query cannot be empty")
	}
	if req.UserID == "" || len(req.Tenants) == 0 {
		return nil, apperrors.NewBadRequestError("target user workspaces are required")
	}
	kbIDs := uniqueNonEmptyStrings(req.KnowledgeBaseIDs)
	knowledgeIDs := uniqueNonEmptyStrings(req.KnowledgeIDs)
	if len(kbIDs) == 0 && len(knowledgeIDs) == 0 {
		return nil, apperrors.NewBadRequestError(
			"at least one knowledge_base_ids or knowledge_ids is required")
	}

	// Batch-fetch KB records once so each workspace can partition the scope
	// by its own access rules (own KB vs org-shared KB).
	kbByID := make(map[string]*types.KnowledgeBase, len(kbIDs))
	if len(kbIDs) > 0 {
		kbs, err := s.knowledgeBaseService.GetKnowledgeBasesByIDsOnly(ctx, kbIDs)
		if err != nil {
			logger.Warnf(ctx, "[dolphin] batch KB fetch failed: %v", err)
		}
		for _, kb := range kbs {
			if kb != nil {
				kbByID[kb.ID] = kb
			}
		}
	}

	// Each workspace is searched through SearchKnowledge, the same path the
	// public knowledge-search API uses, so the tenant RetrievalConfig, the
	// shared-KB authorization and the search → rerank → merge → filter stages
	// behave exactly as they do for an in-workspace search. The Dolphin
	// request overrides map onto that API's options; zero values keep the
	// tenant defaults.
	opts := &types.KnowledgeSearchOptions{
		MatchCount: req.TopK,
		Rerank:     &types.RerankOptions{ModelID: req.RerankModelID},
	}
	if req.VectorThreshold > 0 {
		threshold := req.VectorThreshold
		opts.VectorThreshold = &threshold
	}
	if req.KeywordThreshold > 0 {
		threshold := req.KeywordThreshold
		opts.KeywordThreshold = &threshold
	}
	if req.RerankThreshold > 0 {
		threshold := req.RerankThreshold
		opts.Rerank.Threshold = &threshold
	}

	result := &types.DolphinSearchResult{
		Query:         query,
		SearchResults: []*types.SearchResult{},
		// Initialise as an empty slice (not nil) so an empty response
		// serialises as [] instead of null, matching the no-scope path.
		TenantResults: []types.DolphinSearchTenantResult{},
	}
	var merged []*types.SearchResult

	for _, scope := range req.Tenants {
		if scope.TenantID == 0 {
			continue
		}
		tenantCtx, err := s.tenantUserContext(ctx, scope.TenantID, req.UserID, scope.Role)
		if err != nil {
			return nil, err
		}

		tenantKBIDs, err := s.partitionKBsForTenant(tenantCtx, kbIDs, kbByID, scope)
		if err != nil {
			return nil, err
		}
		tenantKnowledgeIDs, err := s.partitionKnowledgeForTenant(tenantCtx, scope.TenantID, knowledgeIDs)
		if err != nil {
			return nil, err
		}
		if len(tenantKBIDs) == 0 && len(tenantKnowledgeIDs) == 0 {
			continue
		}

		// Run the standard retrieval pipeline as the target user inside this
		// workspace. The acting context built by tenantUserContext is what
		// makes the shared-KB permission checks, the retrieval config lookup
		// and the vector-store resolution use the user's identity and role
		// in this workspace.
		var tenantResults []*types.SearchResult
		retrieval, searchErr := s.SearchKnowledge(
			tenantCtx, tenantKBIDs, tenantKnowledgeIDs, nil, query, opts,
		)
		if searchErr != nil {
			// One workspace failing must not erase results from the others,
			// mirroring the tolerant per-tenant behaviour of the KB listing API.
			logger.Warnf(tenantCtx, "[dolphin] search failed for tenant %d: %v", scope.TenantID, searchErr)
		} else if retrieval != nil {
			tenantResults = retrieval.Results
		}

		merged = append(merged, tenantResults...)

		tenantName := ""
		if t, terr := s.tenantService.GetTenantByID(ctx, scope.TenantID); terr == nil && t != nil {
			tenantName = t.Name
		}
		result.TenantResults = append(result.TenantResults, types.DolphinSearchTenantResult{
			TenantID:    scope.TenantID,
			TenantName:  tenantName,
			ResultCount: len(tenantResults),
		})
	}

	// Combine per-workspace pipeline outputs into one context: dedup (chunk ID
	// + content signature), restore global relevance order, then cap at top K.
	merged = chatpipeline.DeduplicateSearchResults(merged)
	chatpipeline.SortSearchResults(merged)
	topK := req.TopK
	if topK <= 0 {
		topK = s.cfg.Conversation.RerankTopK
	}
	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}

	// Dedup/sort may collapse an empty result set back to a nil slice;
	// normalise it so the response always carries [] for empty results.
	if merged == nil {
		merged = []*types.SearchResult{}
	}
	result.SearchResults = merged
	result.Total = len(merged)
	result.Contexts = chatpipeline.RenderContexts(ctx, merged)
	return result, nil
}

// tenantUserContext derives a request context in which the target user acts
// inside the given workspace. The pipeline's HybridSearch authorization,
// shared-KB permission checks and vector-store resolution all read these
// values, so this is what makes the search run "as the user".
func (s *sessionService) tenantUserContext(
	ctx context.Context,
	tenantID uint64,
	userID string,
	role types.TenantRole,
) (context.Context, error) {
	tenantCtx := context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	tenantCtx = context.WithValue(tenantCtx, types.UserIDContextKey, userID)
	if role.IsValid() {
		tenantCtx = context.WithValue(tenantCtx, types.TenantRoleContextKey, role)
	}
	tenantCtx = context.WithValue(tenantCtx, types.SessionTenantIDContextKey, tenantID)
	// Override the authenticated caller captured by the auth middleware.
	// CallerFromContext prefers CallerContextKey over the TenantID / UserID /
	// TenantRole keys set above, and the /dolphin routes authenticate with a
	// platform API key whose caller carries TenantID 0. Without this override
	// HybridSearch's authorizeKBAccess would judge every KB against the
	// platform principal and reject the whole scope.
	tenantCtx = types.WithCaller(tenantCtx, types.Caller{
		TenantID: tenantID,
		UserID:   userID,
		Role:     role,
	})

	tenant, err := s.tenantService.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load tenant %d: %w", tenantID, err)
	}
	if tenant == nil {
		return nil, fmt.Errorf("tenant %d not found", tenantID)
	}
	return context.WithValue(tenantCtx, types.TenantInfoContextKey, tenant), nil
}

// partitionKBsForTenant keeps only the requested KBs the target user can
// search from the given workspace: own-tenant KBs plus org-shared KBs passing
// the 3-D permission cap for the user's role in that workspace.
func (s *sessionService) partitionKBsForTenant(
	ctx context.Context,
	kbIDs []string,
	kbByID map[string]*types.KnowledgeBase,
	scope types.DolphinSearchTenantScope,
) ([]string, error) {
	if len(kbIDs) == 0 {
		return nil, nil
	}
	var ids []string
	for _, id := range kbIDs {
		kb := kbByID[id]
		if kb == nil || kb.DeletedAt.Valid {
			continue
		}
		accessible := kb.TenantID == scope.TenantID
		if !accessible && s.kbShareService != nil {
			ok, err := s.kbShareService.HasTenantKBPermission(
				ctx, kb.ID, scope.TenantID, scope.Role, types.OrgRoleViewer)
			if err != nil {
				return nil, fmt.Errorf("check KB %s access for tenant %d: %w", kb.ID, scope.TenantID, err)
			}
			accessible = ok
		}
		if accessible {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// partitionKnowledgeForTenant resolves which of the requested knowledge files
// are visible to the user from the given workspace (own tenant + shared-KB
// files), using the same access path as buildSearchTargets.
func (s *sessionService) partitionKnowledgeForTenant(
	ctx context.Context,
	tenantID uint64,
	knowledgeIDs []string,
) ([]string, error) {
	if len(knowledgeIDs) == 0 {
		return nil, nil
	}
	list, err := s.knowledgeService.GetKnowledgeBatchWithSharedAccess(ctx, tenantID, knowledgeIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge access for tenant %d: %w", tenantID, err)
	}
	seen := make(map[string]bool)
	var ids []string
	for _, k := range list {
		if k == nil || seen[k.ID] {
			continue
		}
		seen[k.ID] = true
		ids = append(ids, k.ID)
	}
	return ids, nil
}
