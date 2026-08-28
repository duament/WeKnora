package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterDolphinRoutes mounts the cross-workspace admin endpoints under
// /dolphin.
//
// These are control-plane style APIs for operators: the caller must be a
// platform system admin (JWT) or present a platform API key carrying a
// system capability. The target user is selected by email and the response
// spans every workspace the user belongs to, so the routes do not read the
// caller's own tenant context — platform API keys may call them WITHOUT an
// X-Tenant-ID header (see isPlatformTenantOptionalAPI in middleware/auth.go).
//
// API-key policy: PlatformOnly + system_tenants_read/manage, mirroring the
// other cross-workspace tenant-inspection endpoints (/tenants/all,
// /tenants/search). Workspace-bound keys are rejected regardless of their
// workspace capabilities.
func RegisterDolphinRoutes(
	r *gin.RouterGroup,
	dolphin *handler.DolphinHandler,
	g *rbacGuards,
) {
	if dolphin == nil {
		return
	}
	dolphinGroup := r.Group("/dolphin", g.SystemAdmin())
	{
		g.apiKeyRoute(dolphinGroup, http.MethodGet, "/knowledge-bases",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
			dolphin.ListUserKnowledgeBases)
		g.apiKeyRoute(dolphinGroup, http.MethodGet, "/users/:email/knowledge-bases",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
			dolphin.ListUserKnowledgeBases)
		// Search as the target user: the caller understands the user's
		// intent, then asks for processed RAG context from their KBs.
		g.apiKeyRoute(dolphinGroup, http.MethodPost, "/search-context",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
			dolphin.SearchKnowledgeContext)
		g.apiKeyRoute(dolphinGroup, http.MethodPost, "/users/:email/search-context",
			apiKeyPlatform(types.APIKeyCapabilitySystemTenantsRead, types.APIKeyCapabilitySystemTenantsManage),
			dolphin.SearchKnowledgeContext)
	}
}
