package session

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetArtifactUsage reports logical bytes for the caller's visible artifacts.
func (h *Handler) GetArtifactUsage(c *gin.Context) {
	service, ok := h.messageService.(interface {
		ArtifactUsage(context.Context) (int64, int64, error)
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false})
		return
	}
	count, bytes, err := service.ArtifactUsage(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"file_count": count, "logical_bytes": bytes, "scope": "accessible_artifact_versions",
	}})
}
