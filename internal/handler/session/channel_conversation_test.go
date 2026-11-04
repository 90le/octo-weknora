package session

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChannelConversationHidesAssistantThoughts(t *testing.T) {
	rows := []types.ChannelConversationMessage{
		{Role: "assistant", Content: "<think>secret</think>answer"},
		{Role: "assistant", Content: "answer<think>unfinished secret"},
		{Role: "user", Content: "<think>user text</think>"},
	}
	stripChannelConversationThoughts(rows)
	require.Equal(t, "answer", rows[0].Content)
	require.Equal(t, "answer", rows[1].Content)
	require.Equal(t, "<think>user text</think>", rows[2].Content)
}
