package im

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/octobusiness"
)

func TestBusinessConfirmationUsesNativeCommandNotRenamableChannelLabel(t *testing.T) {
	command := "确认 OP-00000000-0000-0000-0000-000000000001"
	msg := &IncomingMessage{Platform: "octo", UserID: "real-user", MessageID: "native-message", Content: "@实际机器人名字 " + command, ChatType: ChatTypeGroup, Extra: map[string]string{"octo_addressed": "true", "octo_command_text": command, "octo_group_id": "group"}}
	scope := &ExecutionScope{AccountID: "account", ScopeID: "scope", KnowledgeBaseIDs: []string{"kb"}, Revision: "r"}
	req := &qaRequest{msg: msg, scope: scope, channelID: "channel", channel: &IMChannel{ID: "channel", TenantID: 1, Platform: "octo", Name: "用户随意起的本地渠道名"}, adapter: &scopeTestAdapter{scope: scope}}
	ctx := (&Service{}).businessContext(context.Background(), req, scope)
	p, ok := octobusiness.PrincipalFromContext(ctx)
	if !ok || p.MessageText != command || p.UserID != "real-user" {
		t.Fatalf("trusted confirmation lost: %+v", p)
	}
	if msg.Content != "@实际机器人名字 "+command {
		t.Fatal("retrieval query unexpectedly rewritten")
	}
}
