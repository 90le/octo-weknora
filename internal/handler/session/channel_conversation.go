package session

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ReadChannelConversation grants no session mutation or normal session access.
func (h *Handler) ReadChannelConversation(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	position, err := strconv.Atoi(c.Param("index"))
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	page := 0
	if p := c.Query("page"); p != "" {
		page, err = strconv.Atoi(p)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
	}
	if position < 0 || page < 0 || page > 10000 {
		c.Status(http.StatusBadRequest)
		return
	}
	reader, ok := h.messageService.(interface {
		ReadChannelConversation(
			context.Context, string, string, int, int,
		) ([]types.ChannelConversationMessage, int, bool, error)
	})
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	rows, actualPage, hasMore, err := reader.ReadChannelConversation(
		c.Request.Context(), paramSessionID(c), c.Param("message_id"), position, page,
	)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(http.StatusInternalServerError)
		return
	}
	if err != nil || len(rows) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	stripChannelConversationThoughts(rows)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows, "page": actualPage, "has_more": hasMore})
}

func stripChannelConversationThoughts(rows []types.ChannelConversationMessage) {
	for i := range rows {
		if rows[i].Role == "assistant" {
			rows[i].Content = agenttools.StripThinkBlocks(rows[i].Content)
		}
	}
}
