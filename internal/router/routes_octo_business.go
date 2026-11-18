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
	a.POST("/issues/batch", h.BatchIssues)
	a.GET("/issues/:id", h.GetIssue)
	a.PATCH("/issues/:id", h.UpdateIssue)
	a.POST("/issues/:id/retest", h.RetestIssue)
	a.GET("/issues/:id/replies", h.IssueReplies)
	a.POST("/issues/:id/replies", h.PreviewIssueReply)
	a.POST("/issues/:id/replies/:reply/send", h.SendIssueReply)
	a.GET("/quality", h.QualityReview)
	a.POST("/quality/batch", h.BatchQuality)
	a.GET("/quality/settings", h.QualitySettings)
	a.PUT("/quality/settings", h.PutQualitySettings)
	a.POST("/quality/analyze", h.AnalyzeQuality)
	a.POST("/quality/:id/confirm", h.ConfirmQuality)
	a.GET("/contacts", h.Contacts)
	a.PUT("/contacts", h.PutContact)
	a.DELETE("/contacts/:id", h.DeleteContact)
	a.GET("/report", h.Report)
	a.GET("/report-schedules", h.ReportSchedules)
	a.POST("/report-schedules", h.PutReportSchedule)
	a.POST("/report-schedules/preview", h.PreviewReportSchedule)
	a.PUT("/report-schedules/:id", h.PutReportSchedule)
	a.DELETE("/report-schedules/:id", h.DeleteReportSchedule)
	a.GET("/report-schedules/:id/runs", h.ReportHistory)
	a.POST("/report-schedules/:id/runs", h.GenerateReport)
	a.GET("/report-schedules/:id/runs/:run", h.ReportRunDetail)
	a.POST("/report-schedules/:id/runs/:run/send", h.SendReportRun)
}
