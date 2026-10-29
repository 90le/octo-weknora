package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// LocalDeletionReview returns the last complete scan's pending review, scoped
// through the same workspace and knowledge-base checks as datasource settings.
func (h *DataSourceHandler) LocalDeletionReview(c *gin.Context) {
	tenant := h.getTenantID(c)
	if tenant == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ds, status, msg := h.getOwnedDataSource(c.Request.Context(), tenant, c.Param("id"))
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	if ds.Type != localfolder.Type || !ds.SyncDeletions {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deletion review is unavailable"})
		return
	}
	cursor, err := ds.ParseSyncCursor()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "invalid source cursor"})
		return
	}
	review, err := localfolder.DeletionReviewFromCursor(cursor)
	if err != nil || review == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no deletion review is available"})
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > len(review.Paths) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return
	}
	end := min(offset+100, len(review.Paths))
	c.JSON(
		http.StatusOK,
		gin.H{
			"digest":      review.Digest,
			"count":       len(review.Paths),
			"paths":       review.Paths[offset:end],
			"next_offset": end,
			"has_more":    end < len(review.Paths),
		},
	)
}

// ConfirmLocalDeletionReview queues a sync using the reviewed deletion digest.
func (h *DataSourceHandler) ConfirmLocalDeletionReview(c *gin.Context) {
	tenant := h.getTenantID(c)
	if tenant == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if _, status, msg := h.getOwnedDataSource(c.Request.Context(), tenant, c.Param("id")); status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	var req struct {
		Digest string `json:"digest"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Digest) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid deletion review"})
		return
	}
	service, ok := h.service.(interface {
		ConfirmLocalDeletionReview(context.Context, string, string) (*types.SyncLog, error)
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "deletion review is unavailable"})
		return
	}
	log, err := service.ConfirmLocalDeletionReview(c.Request.Context(), c.Param("id"), req.Digest)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, log)
}
