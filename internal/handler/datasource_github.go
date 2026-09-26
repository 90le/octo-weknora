package handler

import (
	"context"
	"net/http"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type githubScheduleMigrationService interface {
	PreviewGitHubScheduleMigration(context.Context, *types.GitHubScheduleMigrationPreviewRequest) (*types.GitHubScheduleMigrationPreviewResponse, error)
	ApplyGitHubScheduleMigration(context.Context, *types.GitHubScheduleMigrationApplyRequest) (*types.GitHubScheduleMigrationApplyResponse, error)
}

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

// PreviewGitHubScheduleMigration shows the exact current and proposed cron for
// one authorized KB. Opening the preview does not change sources or start sync.
func (h *DataSourceHandler) PreviewGitHubScheduleMigration(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req types.GitHubScheduleMigrationPreviewRequest
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid schedule preview request"})
		return
	}
	if _, status, message := h.getOwnedKnowledgeBase(c.Request.Context(), tenantID, req.KnowledgeBaseID); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	service, ok := h.service.(githubScheduleMigrationService)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "schedule migration unavailable"})
		return
	}
	req.TenantID = tenantID
	result, err := service.PreviewGitHubScheduleMigration(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "schedule preview failed"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ApplyGitHubScheduleMigration changes only explicitly selected rows that
// still match their preview. Every row is independently version-checked.
func (h *DataSourceHandler) ApplyGitHubScheduleMigration(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req types.GitHubScheduleMigrationApplyRequest
	if c.ShouldBindJSON(&req) != nil || len(req.Selections) == 0 || len(req.Selections) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid schedule migration request"})
		return
	}
	if _, status, message := h.getOwnedKnowledgeBase(c.Request.Context(), tenantID, req.KnowledgeBaseID); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	service, ok := h.service.(githubScheduleMigrationService)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "schedule migration unavailable"})
		return
	}
	req.TenantID = tenantID
	result, err := service.ApplyGitHubScheduleMigration(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "schedule migration selection is invalid"})
		return
	}
	c.JSON(http.StatusOK, result)
}
