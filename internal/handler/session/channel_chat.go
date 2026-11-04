package session

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *Handler) ReadChannelChat(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 {
		c.Status(http.StatusBadRequest)
		return
	}
	var before *time.Time
	if value := c.Query("before_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		before = &parsed
	}
	beforeID := c.Query("before_id")
	if beforeID != "" && (before == nil || len(beforeID) > 36) {
		c.Status(http.StatusBadRequest)
		return
	}
	reader, ok := h.messageService.(interface {
		ReadChannelChat(context.Context, string, int, *time.Time, string) (*types.Session, []*types.Message, error)
	})
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	session, messages, err := reader.ReadChannelChat(c.Request.Context(), paramSessionID(c), limit, before, beforeID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	for _, msg := range messages {
		stripChannelChatThoughts(msg)
	}
	h.filterChannelChatReferences(c.Request.Context(), messages)
	if messages == nil {
		messages = []*types.Message{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"session": session, "messages": messages}})
}

func stripChannelChatThoughts(msg *types.Message) {
	if msg.Role == "assistant" {
		msg.Content = agenttools.StripThinkBlocks(msg.Content)
	}
	visible := make(map[string]bool)
	for _, ref := range types.ScanResourceReferences(msg.Content) {
		visible[ref] = true
	}
	for i := range msg.Artifacts {
		if !visible[msg.Artifacts[i].URL] {
			msg.Artifacts[i].URL = ""
		}
	}
}

func (h *Handler) filterChannelChatReferences(ctx context.Context, messages []*types.Message) {
	lookup, ok := h.messageService.(interface {
		ChannelChatKnowledge(context.Context, string) (*types.Knowledge, error)
	})
	caller := types.CallerFromContext(ctx)
	allowed := make(map[string]*types.Knowledge)
	checked := make(map[string]bool)
	for _, message := range messages {
		var safe types.References
		if ok && h.knowledgebaseService != nil && message.Role == "assistant" && caller.UserID != "" {
			for _, ref := range message.KnowledgeReferences {
				if ref == nil || ref.KnowledgeID == "" {
					continue
				}
				if !checked[ref.KnowledgeID] {
					checked[ref.KnowledgeID] = true
					knowledge, err := lookup.ChannelChatKnowledge(ctx, ref.KnowledgeID)
					if err == nil && knowledge != nil && knowledge.ID == ref.KnowledgeID &&
						knowledge.KnowledgeBaseID != "" {
						kb, err := h.knowledgebaseService.GetKnowledgeBaseByIDOnly(ctx, knowledge.KnowledgeBaseID)
						if err == nil && kb != nil && kb.ID == knowledge.KnowledgeBaseID &&
							kb.TenantID == knowledge.TenantID {
							_, err = access.ResolveKB(
								ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleViewer,
								h.kbShareService, h.agentShareService,
							)
							if err == nil {
								allowed[ref.KnowledgeID] = knowledge
							}
						}
					}
				}
				knowledge := allowed[ref.KnowledgeID]
				if knowledge == nil || (ref.KnowledgeBaseID != "" && ref.KnowledgeBaseID != knowledge.KnowledgeBaseID) {
					continue
				}
				// Return only fields used for citation display and navigation.
				// Arbitrary metadata, embedded image handles and source URLs are
				// not a grant to fetch additional resources.
				safe = append(safe, &types.SearchResult{
					ID: ref.ID, KnowledgeID: knowledge.ID, KnowledgeBaseID: knowledge.KnowledgeBaseID,
					KnowledgeTitle: ref.KnowledgeTitle, KnowledgeFilename: ref.KnowledgeFilename,
					Content: ref.Content, ChunkIndex: ref.ChunkIndex, Seq: ref.Seq,
					StartAt: ref.StartAt, EndAt: ref.EndAt, Score: ref.Score, ChunkType: ref.ChunkType,
				})
			}
		}
		message.KnowledgeReferences = safe
	}
}
