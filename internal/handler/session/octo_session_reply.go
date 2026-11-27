package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	maxOctoBridgeFilesPerSend = 5
	maxOctoBridgeFileBytes    = 20 << 20
)

// OctoSessionBridge is implemented by im.OctoSessionBridgeService and wired
// from container.go so session handlers can enrich Octo provenance and send
// Web replies & files back along the session's original Octo return path.
type OctoSessionBridge interface {
	ResolveSessionOrigins(ctx context.Context, tenantID uint64, sessionIDs []string) map[string]*types.SessionIMOrigin
	SendToSessionOrigin(ctx context.Context, tenantID uint64, sessionID, content string, quoteLatest bool) (receipt string, origin *types.SessionIMOrigin, err error)
	SendFileToSessionOrigin(ctx context.Context, tenantID uint64, sessionID, fileName string, data []byte, contentType string, quoteLatest bool) (receipt string, sentFileName string, origin *types.SessionIMOrigin, err error)
}

type sentOctoFile struct {
	Name     string `json:"name"`
	SentName string `json:"sent_name"`
	Receipt  string `json:"receipt"`
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

// SendOctoManualReply sends a human reply (text and/or file attachments) from
// the Web chat UI directly back to the originating Octo DM/group/subarea using
// the bound Bot identity, and persists an assistant message in the session so
// the transcript stays in sync.
//
// Accepts either application/json (text-only) or multipart/form-data (text +
// up to 5 files, <=20MB each; unsupported non-executable extensions are
// automatically packaged as .zip by the Octo adapter).
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

	type uploadPayload struct {
		name        string
		contentType string
		data        []byte
	}
	var content string
	quoteLatest := true
	var uploads []uploadPayload

	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "上传表单解析失败"})
			return
		}
		content = strings.TrimSpace(c.PostForm("content"))
		if rawQuote := strings.TrimSpace(c.PostForm("quote_latest")); rawQuote == "false" || rawQuote == "0" {
			quoteLatest = false
		}
		if c.Request.MultipartForm != nil {
			fileHeaders := c.Request.MultipartForm.File["files"]
			if len(fileHeaders) == 0 {
				fileHeaders = c.Request.MultipartForm.File["file"]
			}
			if len(fileHeaders) > maxOctoBridgeFilesPerSend {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("单次最多上传 %d 个附件", maxOctoBridgeFilesPerSend)})
				return
			}
			for _, fh := range fileHeaders {
				if fh.Size <= 0 || fh.Size > maxOctoBridgeFileBytes {
					c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("附件「%s」为空或超过 20MB 限制", fh.Filename)})
					return
				}
				f, openErr := fh.Open()
				if openErr != nil {
					c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "读取附件失败"})
					return
				}
				data, readErr := io.ReadAll(io.LimitReader(f, maxOctoBridgeFileBytes+1))
				_ = f.Close()
				if readErr != nil || len(data) == 0 || len(data) > maxOctoBridgeFileBytes {
					c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("附件「%s」读取失败或超过 20MB", fh.Filename)})
					return
				}
				uploads = append(uploads, uploadPayload{
					name:        fh.Filename,
					contentType: fh.Header.Get("Content-Type"),
					data:        data,
				})
			}
		}
	} else {
		var req struct {
			Content     string `json:"content"`
			QuoteLatest *bool  `json:"quote_latest"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "请求格式无效"})
			return
		}
		content = strings.TrimSpace(req.Content)
		if req.QuoteLatest != nil {
			quoteLatest = *req.QuoteLatest
		}
	}

	if (content == "" && len(uploads) == 0) || len(content) > 20000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "回复内容与附件不能同时为空，正文不超过 20000 字"})
		return
	}

	var receipt string
	var origin *types.SessionIMOrigin
	if content != "" {
		receipt, origin, err = h.octoBridge.SendToSessionOrigin(ctx, sess.TenantID, sessionID, content, quoteLatest)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "发送文本到 Octo 失败：" + err.Error()})
			return
		}
	}

	sentFiles := make([]sentOctoFile, 0, len(uploads))
	for _, up := range uploads {
		fReceipt, sentName, fOrigin, fErr := h.octoBridge.SendFileToSessionOrigin(
			ctx, sess.TenantID, sessionID, up.name, up.data, up.contentType, quoteLatest && content == "",
		)
		if fOrigin != nil {
			origin = fOrigin
		}
		if fErr != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"success":    false,
				"error":      fmt.Sprintf("附件「%s」发送到 Octo 失败：%v", up.name, fErr),
				"sent_files": sentFiles,
			})
			return
		}
		if receipt == "" {
			receipt = fReceipt
		}
		sentFiles = append(sentFiles, sentOctoFile{Name: up.name, SentName: sentName, Receipt: fReceipt})
	}

	storedContent := content
	if len(sentFiles) > 0 {
		lines := make([]string, 0, len(sentFiles))
		for _, sf := range sentFiles {
			if sf.SentName != "" && sf.SentName != sf.Name {
				lines = append(lines, fmt.Sprintf("- 📎 `%s`（已自动打包为 `%s` 发送）", sf.Name, sf.SentName))
			} else {
				lines = append(lines, fmt.Sprintf("- 📎 `%s`", sf.Name))
			}
		}
		block := "**已随人工回复发送附件：**\n" + strings.Join(lines, "\n")
		if storedContent != "" {
			storedContent += "\n\n" + block
		} else {
			storedContent = block
		}
	}

	now := time.Now().UTC()
	msg := &types.Message{
		ID:          uuid.NewString(),
		RequestID:   "octo-manual:" + receipt,
		SessionID:   sessionID,
		Role:        "assistant",
		Content:     storedContent,
		Channel:     "octo_manual",
		IsCompleted: true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	created, err := h.messageService.CreateMessage(ctx, msg)
	if err != nil {
		created = msg
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message":    created,
			"receipt":    receipt,
			"sent_files": sentFiles,
			"origin":     origin,
		},
	})
}

// RelayMessageToOcto delivers an existing completed assistant message (full
// multi-paragraph text + all generated sandbox artifacts) from the Web chat
// session back to the originating Octo DM/group/subarea along the latest Octo
// message's return path, with idempotency protection against duplicate sends.
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
	if msg.Role != "assistant" || !msg.IsCompleted {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "仅支持回传已完成的 AI 答复消息"})
		return
	}
	if msg.Channel == "octo_relayed" || msg.Channel == "octo_manual" {
		c.JSON(http.StatusConflict, gin.H{"success": false, "error": "该消息已发送至原 Octo 会话，无需重复发送"})
		return
	}

	fullText := strings.TrimSpace(msg.Content)
	if fullText == "" && len(msg.Artifacts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "消息正文与产物均为空"})
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

	var receipt string
	var origin *types.SessionIMOrigin
	if fullText != "" {
		receipt, origin, err = h.octoBridge.SendToSessionOrigin(ctx, sess.TenantID, sessionID, fullText, quoteLatest)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "正文回传到 Octo 失败：" + err.Error()})
			return
		}
	}

	// Deliver all non-deleted generated artifacts attached to this assistant turn
	sentFiles := make([]sentOctoFile, 0, len(msg.Artifacts))
	var fileErrors []string
	if h.fileService != nil && len(msg.Artifacts) > 0 {
		for idx, art := range msg.Artifacts {
			if art.Deleted() || art.URL == "" {
				continue
			}
			resolved, rErr := access.ResolveMessageArtifact(
				ctx, msg, idx, h.agentShareService, h.resourceCatalog,
				access.MessageKBShareAuthorizer{ShareGuard: h.kbShareService, KBs: h.knowledgebaseService},
			)
			if rErr != nil {
				fileErrors = append(fileErrors, fmt.Sprintf("%s: 无权访问产物", art.FileName))
				continue
			}
			fSvc, fCtx, ok := h.resolveArtifactFileService(ctx, resolved.OwnerTenantID, resolved.Path, resolved.StorageBackendID, "octo relay")
			if !ok {
				fileErrors = append(fileErrors, fmt.Sprintf("%s: 存储不可用", art.FileName))
				continue
			}
			reader, gErr := fSvc.GetFile(fCtx, resolved.Path)
			if gErr != nil {
				fileErrors = append(fileErrors, fmt.Sprintf("%s: 读取失败", art.FileName))
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(reader, maxOctoBridgeFileBytes+1))
			_ = reader.Close()
			if readErr != nil || len(data) == 0 || len(data) > maxOctoBridgeFileBytes {
				fileErrors = append(fileErrors, fmt.Sprintf("%s: 文件为空或超过 20MB", art.FileName))
				continue
			}
			fReceipt, sentName, fOrigin, sErr := h.octoBridge.SendFileToSessionOrigin(
				ctx, sess.TenantID, sessionID, art.FileName, data, mimeTypeFor(art.FileName), false,
			)
			if fOrigin != nil {
				origin = fOrigin
			}
			if sErr != nil {
				fileErrors = append(fileErrors, fmt.Sprintf("%s: %v", art.FileName, sErr))
				continue
			}
			if receipt == "" {
				receipt = fReceipt
			}
			sentFiles = append(sentFiles, sentOctoFile{Name: art.FileName, SentName: sentName, Receipt: fReceipt})
		}
	}

	msg.Channel = "octo_relayed"
	_ = h.messageService.UpdateMessage(ctx, msg)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"message_id":  msg.ID,
			"receipt":     receipt,
			"sent_files":  sentFiles,
			"file_errors": fileErrors,
			"origin":      origin,
		},
	})
}
