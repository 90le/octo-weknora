package session

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OctoSessionBridge is implemented by im.OctoSessionBridgeService and wired
// from container.go so session handlers can enrich Octo provenance and send
// Web replies back along the session's original Octo return path without an
// import cycle.
type OctoSessionBridge interface {
	ResolveSessionOrigins(ctx context.Context, tenantID uint64, sessionIDs []string) map[string]*types.SessionIMOrigin
	SendToSessionOrigin(ctx context.Context, tenantID uint64, sessionID, content string, quoteLatest bool) (receipt string, origin *types.SessionIMOrigin, err error)
}

// SetOctoSessionBridge attaches the optional Octo session bridge.
func (h *Handler) SetOctoSessionBridge(bridge OctoSessionBridge) {
	h.octoBridge = bridge
}

func (h *Handler) canOperateOctoSession(ctx context.Context, sessionID string) bool {
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 || caller.UserID == "" {
		return false
	}
	if caller.Role.HasPermission(types.TenantRoleAdmin) {
		return true
	}
	return h.canContinueOctoChat(ctx, sessionID)
}

// SendOctoManualReply sends a human reply from the Web chat UI directly back
// to the originating Octo DM/group/subarea using the bound Bot identity, and
// persists an assistant message in the session so the transcript stays in sync.
func (h *Handler) SendOctoManualReply(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Param("id"))
	}
	if sessionID == "" {
		c.Error(errors.NewBadRequestError("session_id is required"))
		return
	}
	if h.octoBridge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "Octo session bridge unavailable"})
		return
	}
	if !h.canOperateOctoSession(ctx, sessionID) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "需要空间管理员或渠道绑定所有者权限"})
		return
	}
	sess, err := h.sessionService.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "session not found"})
		return
	}
	if sess.IMPlatform != "octo" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "仅支持 Octo 渠道会话原路回复"})
		return
	}

	var req struct {
		Content     string `json:"content"`
		QuoteLatest *bool  `json:"quote_latest"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Content) == "" || len(req.Content) > 8000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "回复内容不能为空且不超过 8000 字"})
		return
	}
	quoteLatest := true
	if req.QuoteLatest != nil {
		quoteLatest = *req.QuoteLatest
	}

	receipt, origin, err := h.octoBridge.SendToSessionOrigin(ctx, sess.TenantID, sessionID, req.Content, quoteLatest)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "发送到 Octo 失败：" + err.Error()})
		return
	}

	now := time.Now().UTC()
	msg := &types.Message{
		ID:          uuid.NewString(),
		RequestID:   "octo-manual:" + receipt,
		SessionID:   sessionID,
		Role:        "assistant",
		Content:     strings.TrimSpace(req.Content),
		Channel:     "octo_manual",
		IsCompleted: true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	created, err := h.messageService.CreateMessage(ctx, msg)
	if err != nil {
		// Octo delivery already succeeded; still return receipt cleanly.
		created = msg
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message": created,
			"receipt": receipt,
			"origin":  origin,
		},
	})
}

// RelayMessageToOcto delivers an existing completed assistant message from the
// Web chat session back to the originating Octo DM/group/subarea along the
// latest Octo message's return path, with idempotency protection against
// accidental duplicate sends.
func (h *Handler) RelayMessageToOcto(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.Param("id"))
	}
	messageID := strings.TrimSpace(c.Param("message_id"))
	if sessionID == "" || messageID == "" {
		c.Error(errors.NewBadRequestError("session_id and message_id are required"))
		return
	}
	if h.octoBridge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "Octo session bridge unavailable"})
		return
	}
	if !h.canOperateOctoSession(ctx, sessionID) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "需要空间管理员或渠道绑定所有者权限"})
		return
	}
	sess, err := h.sessionService.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "session not found"})
		return
	}
	if sess.IMPlatform != "octo" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "仅支持 Octo 渠道会话原路回传"})
		return
	}

	msg, err := h.messageService.GetMessage(ctx, sessionID, messageID)
	if err != nil || msg == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "message not found"})
		return
	}
	if msg.Role != "assistant" || !msg.IsCompleted || strings.TrimSpace(msg.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "仅支持回传已完成的 AI 答复消息"})
		return
	}
	if msg.Channel == "octo_relayed" || msg.Channel == "octo_manual" {
		c.JSON(http.StatusConflict, gin.H{"success": false, "error": "该消息已发送至原 Octo 会话，无需重复发送"})
		return
	}

	var req struct {
		QuoteLatest *bool `json:"quote_latest"`
	}
	if c.Request.Body != nil && c.Request.ContentLength > 0 {
		_ = c.ShouldBindJSON(&req)
	}
	quoteLatest := true
	if req.QuoteLatest != nil {
		quoteLatest = *req.QuoteLatest
	}

	receipt, origin, err := h.octoBridge.SendToSessionOrigin(ctx, sess.TenantID, sessionID, msg.Content, quoteLatest)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "回传到 Octo 失败：" + err.Error()})
		return
	}

	msg.Channel = "octo_relayed"
	_ = h.messageService.UpdateMessage(ctx, msg)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message_id": msg.ID,
			"receipt":    receipt,
			"origin":     origin,
		},
	})
}
