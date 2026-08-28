package router

import (
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRegisterDolphinRoutesDeclaresSearchContext guards route registration and
// the API-key policy for the cross-workspace "search as user" endpoint.
func TestRegisterDolphinRoutesDeclaresSearchContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{}}}
	engine := gin.New()
	v1 := engine.Group("/api/v1")
	RegisterDolphinRoutes(v1, &handler.DolphinHandler{}, g)

	want := map[string]bool{
		http.MethodPost + " /api/v1/dolphin/search-context":              true,
		http.MethodPost + " /api/v1/dolphin/users/:email/search-context": true,
		http.MethodGet + " /api/v1/dolphin/knowledge-bases":              true,
	}
	registered := map[string]bool{}
	for _, ri := range engine.Routes() {
		registered[ri.Method+" "+ri.Path] = true
	}
	for route := range want {
		require.True(t, registered[route], "route not registered: %s", route)
	}

	// The search endpoints must be declared to the API-key gate as
	// platform-only routes with system tenant capabilities.
	authorizerRegistered := map[string]bool{}
	for method, paths := range g.apiKeyAuthorizer.RegisteredRoutes() {
		for _, path := range paths {
			authorizerRegistered[method+" "+path] = true
		}
	}
	require.True(t, authorizerRegistered["POST /api/v1/dolphin/search-context"])
	require.True(t, authorizerRegistered["POST /api/v1/dolphin/users/:email/search-context"])
}
