package router

import (
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// RegisterOctoRoutes exposes workspace-admin configuration only. Public Agent
// tools must not use these routes: native Octo identity mapping is a separate
// integration step. In particular, supplying a scope ID is not authentication.
func RegisterOctoRoutes(r *gin.RouterGroup, h *octointegration.Handler, g *rbacGuards) {
	// Reuse native RBAC but never inherit its legacy log-only rollout mode.
	cfg := config.Config{}
	if g.cfg != nil {
		cfg = *g.cfg
	}
	tenantConfig := config.TenantConfig{}
	if cfg.Tenant != nil {
		tenantConfig = *cfg.Tenant
	}
	enforce := true
	tenantConfig.EnableRBAC = &enforce
	cfg.Tenant = &tenantConfig
	admin := r.Group("/octo", middleware.RequireRole(types.TenantRoleAdmin, &cfg), octointegration.WorkspaceRequired)
	// No API-key route policy is registered: the native API-key gate denies
	// these admin configuration endpoints, including for full-access keys.
	admin.GET("/scopes", h.List)
	admin.GET("/connections", h.Connections)
	admin.PUT("/connections/:account_id/credentials", h.PutConnection)
	admin.POST("/scopes", h.Create)
	admin.PUT("/scopes/:scope_id", h.Update)
	admin.POST("/scopes/:scope_id/sync", h.SyncName)
	admin.GET("/scopes/:scope_id/members/:uid/role", h.MemberRole)
	admin.GET("/scopes/:scope_id/effective-bindings", h.Effective)
	admin.PUT("/scopes/:scope_id/knowledge-bases/:id", g.KBAccessWrite("id"), h.Bind)
	admin.DELETE("/scopes/:scope_id/knowledge-bases/:id", g.KBAccessWrite("id"), h.Unbind)
	admin.GET("/knowledge-bases/:id/scopes", g.KBAccessRead("id"), h.Uses)
}
