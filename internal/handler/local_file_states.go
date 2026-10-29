package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// LocalFileStates lists the most recent observations for the current source
// selection. Paths are relative to the granted directory, never host paths.
func (h *DataSourceHandler) LocalFileStates(c *gin.Context) {
	tenant := h.getTenantID(c)
	if tenant == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, tenant)
	if _, status, msg := h.getOwnedDataSource(ctx, tenant, c.Param("id")); status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 1000000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return
	}
	service, ok := h.service.(interface {
		ListLocalFileStates(context.Context, string, string, int) (*types.LocalSourceFilePage, error)
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "file status unavailable"})
		return
	}
	page, err := service.ListLocalFileStates(ctx, c.Param("id"), c.Query("status"), offset)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file status unavailable"})
		return
	}
	c.JSON(http.StatusOK, page)
}
