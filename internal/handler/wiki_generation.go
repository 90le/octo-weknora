package handler

import (
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

// ListGenerationTasks reads generation failures and retry progress for a Wiki.
func (h *WikiPageHandler) ListGenerationTasks(c *gin.Context) {
	kbID, tenantID, err := h.validateWikiKB(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	cursor := c.Query("cursor")
	if len(cursor) > 36 {
		_ = c.Error(errors.NewBadRequestError("Invalid Wiki task cursor"))
		return
	}
	ctx := c.Request.Context()
	result, err := h.generation.List(ctx, tenantID, kbID, cursor)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		_ = c.Error(errors.NewInternalServerError("Unable to load Wiki generation tasks"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// RetryGenerationTasks queues one or more eligible failed Wiki documents.
func (h *WikiPageHandler) RetryGenerationTasks(c *gin.Context) {
	kbID, tenantID, err := h.validateWikiKB(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	var request struct {
		KnowledgeIDs []string `json:"knowledge_ids" binding:"required,min=1,max=20,dive,min=1,max=36"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || len(request.KnowledgeIDs) > service.WikiGenerationRetryLimit {
		_ = c.Error(errors.NewBadRequestError("Select 1 to 20 documents to retry"))
		return
	}
	for _, id := range request.KnowledgeIDs {
		if strings.TrimSpace(id) != id {
			_ = c.Error(errors.NewBadRequestError("Invalid document ID"))
			return
		}
	}
	ctx := c.Request.Context()
	result, err := h.generation.Retry(ctx, tenantID, kbID, request.KnowledgeIDs)
	switch {
	case stderrors.Is(err, service.ErrWikiGenerationUnavailable):
		_ = c.Error(errors.NewConflictError("Wiki generation is unavailable in this knowledge base"))
	case stderrors.Is(err, service.ErrWikiGenerationModel):
		_ = c.Error(errors.NewServiceUnavailableError("Check the Wiki generation model in knowledge base settings"))
	case err != nil:
		logger.ErrorWithFields(ctx, err, nil)
		_ = c.Error(errors.NewInternalServerError("Unable to queue Wiki generation tasks"))
	default:
		c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
	}
}
