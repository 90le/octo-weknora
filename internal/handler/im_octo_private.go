package handler

import (
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PreviewOctoPrivatePolicy tests a draft without sending an IM message.
func (h *IMHandler) PreviewOctoPrivatePolicy(c *gin.Context) {
	ctx := c.Request.Context()
	tenant, ok := ctx.Value(types.TenantIDContextKey).(uint64)
	if !ok || tenant == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		ChannelID   string     `json:"channel_id" binding:"max=128"`
		TestUID     string     `json:"test_uid" binding:"max=128"`
		KnowledgeID string     `json:"knowledge_base_id" binding:"max=128"`
		Credentials types.JSON `json:"credentials" binding:"required"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 96<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid Octo private policy request"})
		return
	}
	var current *im.IMChannel
	if req.ChannelID != "" {
		row, err := h.imService.GetChannelByIDAndTenant(req.ChannelID, tenant)
		if err != nil || row.Platform != "octo" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Octo channel not found in this workspace"})
			return
		}
		current = row
	}
	next := &im.IMChannel{
		TenantID: tenant, KnowledgeBaseID: req.KnowledgeID, Credentials: req.Credentials, Platform: "octo",
	}
	out, err := h.imService.OctoPrivatePolicyPreview(ctx, current, next, req.TestUID)
	if err != nil {
		writeOctoPrivateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func writeOctoPrivateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, im.ErrOctoPrivatePreview):
		// An already-open editor from before the rollout may not have a
		// preview button. Tell that editor how to recover without accepting
		// an unreviewed change to the channel's private access.
		c.JSON(http.StatusConflict, gin.H{
			"code":  "octo_private_preview_required",
			"error": "请刷新渠道页面，在「私聊权限」重新预览后保存。",
		})
	case errors.Is(err, im.ErrOctoPrivateInvalid), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or unverified Octo private policy"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to check Octo private policy"})
	}
}
