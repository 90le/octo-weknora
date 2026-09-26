package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type githubBatchScopePreviewService interface {
	PreviewGitHubBatchScope(context.Context, *types.GitHubBatchScopePreviewRequest) (*types.GitHubBatchScopePreviewResponse, error)
}

// PreviewGitHubBatchScope performs one explicit, request-only tree preview for
// a repository that has not yet been configured as a data source. Discovery
// and checkbox selection never invoke this endpoint automatically.
func (h *DataSourceHandler) PreviewGitHubBatchScope(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req types.GitHubBatchScopePreviewRequest
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitHub batch scope preview request"})
		return
	}
	if _, status, message := h.getOwnedKnowledgeBase(c.Request.Context(), tenantID, req.KnowledgeBaseID); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	service, ok := h.service.(githubBatchScopePreviewService)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "GitHub batch scope preview unavailable"})
		return
	}
	req.TenantID = tenantID
	result, err := service.PreviewGitHubBatchScope(c.Request.Context(), &req)
	if err != nil {
		if errors.Is(err, datasource.ErrInvalidConfig) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitHub repository scope"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitHub repository preview unavailable"})
		return
	}
	c.JSON(http.StatusOK, result)
}
