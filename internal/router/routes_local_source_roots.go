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
	admin.POST("/probe", h.Probe)
	admin.POST("", h.Create)
	admin.PUT("/:root_id", h.Update)
	admin.DELETE("/:root_id", h.Delete)
}
