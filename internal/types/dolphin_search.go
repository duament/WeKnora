package types

// DolphinSearchTenantScope identifies one workspace in which the target user
// may act during a cross-workspace "search as user" request. Role is the
// user's role inside that workspace and drives the shared-KB permission cap.
type DolphinSearchTenantScope struct {
	TenantID uint64     `json:"tenant_id"`
	Role     TenantRole `json:"role"`
}

// DolphinSearchRequest carries a cross-workspace "search as user" request.
// The caller (typically an upstream intent-understanding agent) already knows
// the user intent and passes the concrete query plus optional scope here.
//
// UserID and Tenants are resolved by the /dolphin handler from the target
// user's email and workspace memberships; they are not bound from JSON.
type DolphinSearchRequest struct {
	// UserID is the resolved target user ID (filled by the handler).
	UserID string `json:"-"`
	// Tenants lists the workspaces the user belongs to (filled by the handler).
	Tenants []DolphinSearchTenantScope `json:"-"`

	// Query is the already-understood search query.
	Query string `json:"query"`

	// KnowledgeBaseIDs constrains the search to these knowledge bases.
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty"`
	// KnowledgeIDs constrains the search to specific knowledge files.
	KnowledgeIDs []string `json:"knowledge_ids,omitempty"`

	// TopK caps the number of returned context chunks. 0/negative means use
	// the tenant's rerank_top_k (per workspace) and the global config default
	// for the final combined truncation.
	TopK int `json:"top_k,omitempty"`
	// RerankModelID overrides the tenant rerank model.
	RerankModelID string `json:"rerank_model_id,omitempty"`
	// VectorThreshold overrides the tenant vector threshold. 0/negative means
	// use the tenant default.
	VectorThreshold float64 `json:"vector_threshold,omitempty"`
	// KeywordThreshold overrides the tenant keyword threshold. 0/negative means
	// use the tenant default.
	KeywordThreshold float64 `json:"keyword_threshold,omitempty"`
	// RerankThreshold overrides the tenant rerank threshold. 0/negative means
	// use the tenant default.
	RerankThreshold float64 `json:"rerank_threshold,omitempty"`
}

// DolphinSearchTenantResult reports the per-workspace retrieval outcome.
type DolphinSearchTenantResult struct {
	TenantID    uint64 `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
	ResultCount int    `json:"result_count"`
}

// DolphinSearchResult is the processed context returned to the caller: the
// final merged search results plus the rendered RAG context block ready to be
// injected into the caller's own prompt.
type DolphinSearchResult struct {
	Query         string                      `json:"query"`
	SearchResults []*SearchResult             `json:"search_results"`
	Contexts      string                      `json:"contexts"`
	Total         int                         `json:"total"`
	TenantResults []DolphinSearchTenantResult `json:"tenant_results"`
}
