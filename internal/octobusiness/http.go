package octobusiness

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// ConsoleContext uses only authentication middleware state. The initial UI is
// workspace-administrator only; group maintainers use native scoped IM tools.
func ConsoleContext(c *gin.Context) (context.Context, error) {
	caller := types.CallerFromContext(c.Request.Context())
	if caller.TenantID == 0 || caller.UserID == "" || !caller.Role.HasPermission(types.TenantRoleAdmin) {
		return nil, ErrDenied
	}
	if scope, ok := types.TenantAPIKeyScopeFromContext(c.Request.Context()); ok && !scope.FullAccess {
		return nil, ErrDenied
	}
	p := Principal{TenantID: caller.TenantID, UserID: caller.UserID, UserName: caller.UserID, ChannelID: "console", Console: true, CanManageScope: true}
	return WithPrincipal(c.Request.Context(), p), nil
}
func respond(c *gin.Context, data any, err error) {
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
		return
	}
	status := http.StatusInternalServerError
	message := "Unable to complete this knowledge operation"
	switch {
	case errors.Is(err, ErrDenied):
		status = http.StatusForbidden
		message = err.Error()
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrClarify):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, ErrConflict), errors.Is(err, gorm.ErrDuplicatedKey):
		status = http.StatusConflict
		message = "Operation changed; refresh before retrying"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = "Record not found in the authorized scope"
	}
	c.JSON(status, gin.H{"success": false, "error": message})
}
func httpFilter(c *gin.Context) (IssueFilter, error) {
	f := IssueFilter{ScopeID: c.Query("scope_id"), KnowledgeBaseID: c.Query("knowledge_base_id"), Kind: c.Query("kind"), Status: c.Query("status"), Keyword: c.Query("keyword")}
	var err error
	if f.Page, err = strconv.Atoi(c.DefaultQuery("page", "1")); err != nil {
		return f, ErrInvalid
	}
	if f.PageSize, err = strconv.Atoi(c.DefaultQuery("page_size", "30")); err != nil {
		return f, ErrInvalid
	}
	if raw := c.Query("from"); raw != "" {
		v, e := time.Parse(time.RFC3339, raw)
		if e != nil {
			return f, ErrInvalid
		}
		f.From = &v
	}
	if raw := c.Query("to"); raw != "" {
		v, e := time.Parse(time.RFC3339, raw)
		if e != nil {
			return f, ErrInvalid
		}
		f.To = &v
	}
	return f, nil
}
func (h *Handler) ListIssues(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	f, err := httpFilter(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows, err := h.service.ListIssues(ctx, f)
	respond(c, rows, err)
}
func (h *Handler) GetIssue(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	row, events, err := h.service.GetIssue(ctx, c.Param("id"))
	respond(c, gin.H{"issue": row, "events": events}, err)
}
func (h *Handler) UpdateIssue(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input IssueUpdate
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	row, err := h.service.UpdateIssue(ctx, c.Param("id"), input)
	respond(c, row, err)
}
func (h *Handler) Contacts(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows, err := h.service.Contacts(ctx, c.Query("knowledge_base_id"))
	respond(c, rows, err)
}
func (h *Handler) PutContact(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input Contact
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	row, err := h.service.PutContact(ctx, input)
	respond(c, row, err)
}
func (h *Handler) DeleteContact(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	err = h.service.DeleteContact(ctx, c.Param("id"))
	respond(c, gin.H{"deleted": err == nil}, err)
}
func (h *Handler) Report(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	f, err := httpFilter(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	report, err := h.service.Report(ctx, f)
	respond(c, report, err)
}

func (h *Handler) ReportSchedules(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows, err := h.service.ListReportSchedules(ctx)
	respond(c, rows, err)
}
func (h *Handler) PutReportSchedule(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input ReportSchedule
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	input.ID = c.Param("id")
	row, err := h.service.PutReportSchedule(ctx, input)
	respond(c, row, err)
}
func (h *Handler) DeleteReportSchedule(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	err = h.service.DeleteReportSchedule(ctx, c.Param("id"))
	respond(c, gin.H{"deleted": err == nil}, err)
}
