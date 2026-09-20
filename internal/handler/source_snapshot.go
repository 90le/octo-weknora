package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func (h *DataSourceHandler) SourceLocalRoots(c *gin.Context) {
	reader, ok := h.service.(interface {
		LocalSourceRoots(context.Context, uint64) ([]localfolder.Root, error)
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "server folder registry unavailable"})
		return
	}
	roots, err := reader.LocalSourceRoots(c.Request.Context(), h.getTenantID(c))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	items := []gin.H{}
	for _, r := range roots {
		items = append(items, gin.H{"id": r.ID, "name": r.Name})
	}
	c.JSON(http.StatusOK, items)
}
func (h *DataSourceHandler) SourceSnapshots(c *gin.Context) {
	s, ok := h.service.(interfaces.SourceSnapshotReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source reader unavailable"})
		return
	}
	v, err := s.ListSourceSnapshots(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "knowledge source access denied"})
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *DataSourceHandler) SourceSnapshotTree(c *gin.Context) {
	s, ok := h.service.(interfaces.SourceSnapshotReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source reader unavailable"})
		return
	}
	offset, _ := strconv.Atoi(c.Query("offset"))
	v, err := s.SourceTree(c.Request.Context(), c.Param("id"), c.Param("source_id"), c.Query("snapshot_id"), c.Query("directory"), offset)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source snapshot unavailable or access denied"})
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *DataSourceHandler) SourceSnapshotRead(c *gin.Context) {
	s, ok := h.service.(interfaces.SourceSnapshotReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source reader unavailable"})
		return
	}
	start, _ := strconv.Atoi(c.Query("start_line"))
	end, _ := strconv.Atoi(c.Query("end_line"))
	v, err := s.ReadSourceFile(c.Request.Context(), c.Param("id"), c.Param("source_id"), c.Query("snapshot_id"), c.Query("path"), start, end)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source file unavailable or access denied"})
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *DataSourceHandler) SourceSnapshotSearch(c *gin.Context) {
	s, ok := h.service.(interfaces.SourceSnapshotReader)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source reader unavailable"})
		return
	}
	v, err := s.SearchSourceFiles(c.Request.Context(), c.Param("id"), c.Param("source_id"), c.Query("snapshot_id"), c.Query("q"), c.Query("path"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source search unavailable or access denied"})
		return
	}
	c.JSON(http.StatusOK, v)
}
