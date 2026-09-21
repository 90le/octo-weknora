package handler

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// DiscoverGitHubRepositories lists a single safe page of repositories for the
// batch picker. Credentials are request-only and never persisted here.
func (h *DataSourceHandler) DiscoverGitHubRepositories(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req types.GitHubDiscoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitHub discovery request"})
		return
	}
	req.TenantID = tenantID
	result, err := h.service.DiscoverGitHubRepositories(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// CreateGitHubBatch creates bounded ordinary data sources from an explicit
// repository selection. It never creates an implicit organization-wide source.
func (h *DataSourceHandler) CreateGitHubBatch(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req types.GitHubBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid GitHub batch request"})
		return
	}
	if _, status, message := h.getOwnedKnowledgeBase(c.Request.Context(), tenantID, req.KnowledgeBaseID); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	req.TenantID = tenantID
	result, err := h.service.CreateGitHubBatch(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, result)
}
