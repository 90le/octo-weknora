package octobusiness

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// QualityReview reads retained group or subarea question-answer evidence.
func (h *Handler) QualityReview(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	filter := qualityFilter{
		LibraryID: c.Query("library_id"), Keyword: c.Query("keyword"), Outcome: c.Query("filter"),
		Topic: c.Query("topic"), Group: c.Query("group"),
	}
	if filter.Page, err = strconv.Atoi(c.DefaultQuery("page", "1")); err != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	if filter.PageSize, err = strconv.Atoi(c.DefaultQuery("page_size", "20")); err != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	for key, dst := range map[string]*time.Time{"from": &filter.From, "to": &filter.To} {
		if raw := c.Query(key); raw != "" {
			if *dst, err = time.Parse(time.RFC3339, raw); err != nil {
				respond(c, nil, ErrInvalid)
				return
			}
		}
	}
	page, err := h.service.queryQuality(ctx, c.Query("scope_id"), filter)
	respond(c, page, err)
}

// AnalyzeQuality creates a private draft for the current administrator.
func (h *Handler) AnalyzeQuality(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	input := qualityAnalysisInput{}
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	draft, err := h.service.analyzeQuality(ctx, h.models, input)
	respond(c, draft, err)
}

// ConfirmQuality saves a reviewed issue and a selected owner in one operation.
func (h *Handler) ConfirmQuality(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	input := qualityConfirmation{}
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	issue, err := h.service.confirmQuality(ctx, c.Param("id"), input)
	respond(c, issue, err)
}
