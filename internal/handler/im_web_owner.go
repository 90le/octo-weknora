package handler

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/gin-gonic/gin"
)

func (h *IMHandler) BindWebOwner(c *gin.Context)   { h.setWebOwner(c, true) }
func (h *IMHandler) UnbindWebOwner(c *gin.Context) { h.setWebOwner(c, false) }

func (h *IMHandler) GetWebOwner(c *gin.Context) {
	bound, err := h.imService.WebOwnerStatus(c.Request.Context(), c.Param("id"))
	if errors.Is(err, im.ErrWebOwnerForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "channel binding forbidden"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "channel binding failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bound": bound})
}

func (h *IMHandler) setWebOwner(c *gin.Context, bind bool) {
	err := h.imService.SetWebOwner(c.Request.Context(), c.Param("id"), bind, c.FullPath(), c.Request.Method)
	if errors.Is(err, im.ErrWebOwnerForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "channel binding forbidden"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "channel binding failed"})
		return
	}
	c.Status(http.StatusNoContent)
}
