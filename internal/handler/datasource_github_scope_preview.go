package handler

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// PreviewGitHubDocumentScope is a read-only, KB-scoped dry run. The route uses
// OwnedKBOrAdmin + KBAccessWrite, and the service checks the source's exact KB
// and tenant again before it may use that source's stored GitHub credential.
func (h *DataSourceHandler) PreviewGitHubDocumentScope(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	kbID := c.Param("id")
	if _, status, message := h.getOwnedKnowledgeBase(c.Request.Context(), tenantID, kbID); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var req types.GitHubDocumentScopePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SourceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id and a valid preview request are required"})
		return
	}
	preview, err := h.service.PreviewGitHubDocumentScope(c.Request.Context(), tenantID, kbID, &req)
	if err != nil {
		switch {
		case errors.Is(err, datasource.ErrDataSourceNotFound), errors.Is(err, datasource.ErrKnowledgeBaseNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "source or knowledge base not found"})
		case errors.Is(err, datasource.ErrDataSourceInvalid), errors.Is(err, datasource.ErrInvalidConfig):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitHub source preview request"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub scope preview unavailable"})
		}
		return
	}
	if preview == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub scope preview unavailable"})
		return
	}
	switch preview.TreeState {
	case "missing_path":
		c.JSON(http.StatusUnprocessableEntity, preview)
	case "error":
		if preview.ErrorCode == "github_selection_invalid" || preview.ErrorCode == "github_exclusion_invalid" {
			c.JSON(http.StatusBadRequest, preview)
		} else if preview.ErrorCode == "github_preview_timeout" {
			c.JSON(http.StatusGatewayTimeout, preview)
		} else {
			c.JSON(http.StatusBadGateway, preview)
		}
	default:
		c.JSON(http.StatusOK, preview)
	}
}
