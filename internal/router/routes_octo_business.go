package router

import (
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/gin-gonic/gin"
)

// The service repeats native workspace and asset checks; this route does not
// grant Bot identities console administrator authority.
func RegisterOctoBusinessRoutes(r *gin.RouterGroup, h *octobusiness.Handler, g *rbacGuards) {
	a := r.Group("/octo/business", g.Admin())
	a.GET("/issues", h.ListIssues)
	a.GET("/issues/:id", h.GetIssue)
	a.PATCH("/issues/:id", h.UpdateIssue)
	a.GET("/contacts", h.Contacts)
	a.PUT("/contacts", h.PutContact)
	a.DELETE("/contacts/:id", h.DeleteContact)
	a.GET("/report", h.Report)
	a.GET("/report-schedules", h.ReportSchedules)
	a.POST("/report-schedules", h.PutReportSchedule)
	a.PUT("/report-schedules/:id", h.PutReportSchedule)
	a.DELETE("/report-schedules/:id", h.DeleteReportSchedule)
}
