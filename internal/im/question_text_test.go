package im

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/stretchr/testify/require"
)

func TestOctoQuestionDoesNotChangeConfirmationText(t *testing.T) {
	const command = "确认 OP-00000000-0000-0000-0000-000000000001"
	msg := &IncomingMessage{
		Platform: "octo", ChatType: ChatTypeGroup, Content: command + " @Octo 小丘",
		Extra: map[string]string{"octo_addressed": "true", OctoQuestionTextKey: command},
	}
	scope := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}
	req := &qaRequest{msg: msg, channel: &IMChannel{Platform: "octo", Name: "Octo 小丘"}}
	ctx := (&Service{}).businessContext(context.Background(), req, scope)
	principal, ok := octobusiness.PrincipalFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, command+" @Octo 小丘", principal.MessageText)
	require.Equal(t, command, imQuestionText(msg))
}

func TestOctoQuestionWithOnlyMentionAsksForTextOrKeepsFile(t *testing.T) {
	msg := &IncomingMessage{Platform: "octo", Content: "@Bot", Extra: map[string]string{
		"octo_addressed": "true", OctoQuestionTextKey: "",
	}}
	_, empty := emptyIncomingMessageReply(msg)
	require.True(t, empty)
	msg.MessageType, msg.FileName = MessageTypeImage, "image.png"
	_, empty = emptyIncomingMessageReply(msg)
	require.False(t, empty)
	require.Contains(t, imQuestionText(msg), "image.png")
	require.NotContains(t, imQuestionText(msg), "@Bot")
	msg.Extra[OctoQuestionTextKey] = strings.Repeat("😀", maxContentLength+1)
	require.Equal(t, strings.Repeat("😀", maxContentLength), imQuestionText(msg))
}
