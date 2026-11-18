package octobusiness

import "github.com/gin-gonic/gin"

// QualityReview reads retained group or subarea question-answer evidence.
func (h *Handler) QualityReview(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	page, err := h.service.listQuality(ctx, c.Query("scope_id"))
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
