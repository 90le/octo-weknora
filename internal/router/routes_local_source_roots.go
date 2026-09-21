package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// Source registration is a platform operation. A workspace administrator can
// select granted roots using /datasource/local-roots, but cannot grant themselves
// access to another mounted directory. No API accepts an absolute host path.
func RegisterLocalSourceRootRoutes(r *gin.RouterGroup, h *handler.LocalRootHandler, g *rbacGuards) {
	admin := r.Group("/system/admin/local-source-roots", g.SystemAdmin())
	admin.GET("", h.List)
	admin.GET("/spaces", h.Spaces)
	admin.GET("/spaces/discovered", h.DiscoverSpaces)
	admin.POST("/spaces", h.RegisterSpace)
	admin.GET("/spaces/:space_id/directories", h.BrowseSpace)
	admin.POST("/probe", h.Probe)
	admin.POST("", h.Create)
	admin.PUT("/:root_id", h.Update)
	admin.DELETE("/:root_id", h.Delete)
	admin.GET("/:root_id/directories", h.BrowseRoot)
	// Same API-key capability and role as datasource configuration, without
	// exposing space discovery or physical server paths to workspace admins.
	roots := g.apiKeyGroup(r.Group("/datasource/local-roots"), apiKeyManageDataSources(apiKeyFullAccess()))
	roots.GET("/:root_id/directories", g.Admin(), h.BrowseGrantedRoot)
}
