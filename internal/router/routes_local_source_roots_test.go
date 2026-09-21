package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLocalSourceDiscoveryNeverExposesSystemRoutesToTenantAdminsOrAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []types.TenantRole{types.TenantRoleViewer, types.TenantRoleContributor, types.TenantRoleAdmin, types.TenantRoleOwner} {
		off := false
		g := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &off}}, apiKeyAuthorizer: middleware.NewAPIKeyRouteAuthorizer()}
		r := gin.New()
		r.Use(func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
			ctx = context.WithValue(ctx, types.TenantRoleContextKey, role)
			c.Request = c.Request.WithContext(ctx)
		})
		RegisterLocalSourceRootRoutes(r.Group("/api/v1"), &handler.LocalRootHandler{}, g)
		for _, route := range r.Routes() {
			if strings.Contains(route.Path, "/system/admin/") {
				_, declared := g.apiKeyAuthorizer.Lookup(route.Method, route.Path)
				require.False(t, declared)
				url := strings.ReplaceAll(strings.ReplaceAll(route.Path, ":space_id", "x"), ":root_id", "x")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(route.Method, url, nil))
				require.Equal(t, http.StatusForbidden, w.Code)
			} else {
				_, declared := g.apiKeyAuthorizer.Lookup(route.Method, route.Path)
				require.True(t, declared, "authorized root browse must reuse datasource capability")
				if role == types.TenantRoleViewer || role == types.TenantRoleContributor {
					w := httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/datasource/local-roots/x/directories", nil))
					require.Equal(t, http.StatusForbidden, w.Code)
				}
			}
		}
	}
}
