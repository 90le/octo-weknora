package im

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
)

type fileScopeTestAdapter struct {
	*scopeTestAdapter
	sends int
}

func (a *fileScopeTestAdapter) SendGeneratedFile(
	ctx context.Context, _ *IncomingMessage, _ string, _ []byte, _ string,
) error {
	a.sends++
	return octobusiness.ValidateFileDeliveryAuthority(ctx)
}

func TestBusinessMarkdownCapabilityFollowsFullCurrentScope(t *testing.T) {
	base := &ExecutionScope{
		AccountID: "account", ScopeID: "scope", KnowledgeBaseIDs: []string{"kb"}, Revision: "r",
		AllowGeneratedFiles: true, AllowPublicWeb: true, CanManageScope: true,
		ManageKnowledgeBaseIDs: []string{"kb"},
	}
	baseline := *base
	baselineAdapter := &fileScopeTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: &baseline}}
	baselineReq := &qaRequest{
		msg:       &IncomingMessage{UserID: "user", MessageID: "message", Content: "请发送文件", Extra: map[string]string{}},
		channelID: "channel", channel: &IMChannel{TenantID: 1, Platform: "octo"}, adapter: baselineAdapter,
	}
	baselineCtx := (&Service{}).businessContext(context.Background(), baselineReq, base)
	baselineResult, baselineErr := octobusiness.NewFileDeliveryTool().Execute(
		baselineCtx, json.RawMessage(`{"name":"test.md","content":"test"}`),
	)
	if baselineErr != nil || !baselineResult.Success || baselineAdapter.sends != 1 {
		t.Fatalf("legacy Markdown baseline failed: result=%+v sends=%d err=%v", baselineResult, baselineAdapter.sends, baselineErr)
	}
	for _, change := range []func(*ExecutionScope){
		func(s *ExecutionScope) { s.AllowGeneratedFiles = false },
		func(s *ExecutionScope) { s.AllowPublicWeb = false },
		func(s *ExecutionScope) { s.CanManageScope = false },
		func(s *ExecutionScope) { s.Revision = "changed" },
	} {
		current := *base
		adapter := &fileScopeTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: &current}}
		msg := &IncomingMessage{UserID: "user", MessageID: "message", Content: "请发送文件", Extra: map[string]string{}}
		req := &qaRequest{
			msg: msg, channelID: "channel", channel: &IMChannel{TenantID: 1, Platform: "octo"}, adapter: adapter,
		}
		ctx := (&Service{}).businessContext(context.Background(), req, base)
		if !octobusiness.HasFileDeliverySink(ctx) {
			t.Fatal("explicitly enabled file capability missing")
		}
		change(&current)
		result, err := octobusiness.NewFileDeliveryTool().Execute(ctx, json.RawMessage(`{"name":"test.md","content":"test"}`))
		if err != nil || result.Success || adapter.sends != 0 {
			t.Fatalf("file reached sender after scope change: result=%+v sends=%d err=%v", result, adapter.sends, err)
		}
	}
	disabled := *base
	disabled.AllowGeneratedFiles = false
	adapter := &fileScopeTestAdapter{scopeTestAdapter: &scopeTestAdapter{scope: &disabled}}
	req := &qaRequest{
		msg: &IncomingMessage{Extra: map[string]string{}}, channel: &IMChannel{Platform: "octo"}, adapter: adapter,
	}
	if octobusiness.HasFileDeliverySink((&Service{}).businessContext(context.Background(), req, &disabled)) {
		t.Fatal("disabled capability exposed a file sink")
	}
}

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

func TestBusinessContextAddsEvidenceContractForScopedAndDirectOctoTurns(t *testing.T) {
	msg := &IncomingMessage{Platform: "octo", Content: "Claude 支持接入 Octo IM Bot 吗？", ChatType: ChatTypeDirect}
	req := &qaRequest{msg: msg, channel: &IMChannel{Platform: "octo"}}
	direct := (&Service{}).businessContext(context.Background(), req, nil)
	if got := answerevidence.IntentFromContext(direct); got != answerevidence.IntentIntegration {
		t.Fatalf("direct intent = %q, want integration", got)
	}

	command := "这个函数的源码在哪里实现？"
	scopedMsg := &IncomingMessage{Platform: "octo", Content: "@机器人 " + command, Extra: map[string]string{"octo_addressed": "true", "octo_command_text": command}}
	scope := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}
	scopedReq := &qaRequest{msg: scopedMsg, scope: scope, channelID: "channel", channel: &IMChannel{TenantID: 1, Platform: "octo", Name: "机器人"}, adapter: &scopeTestAdapter{scope: scope}}
	scoped := (&Service{}).businessContext(context.Background(), scopedReq, scope)
	if got := answerevidence.IntentFromContext(scoped); got != answerevidence.IntentSource {
		t.Fatalf("scoped intent = %q, want source", got)
	}
}

func TestBusinessContextKeepsOrdinaryGroupQuestionWhenGroupRulesMentionReleases(t *testing.T) {
	command := "Octo 和 Loop 的关系是什么？"
	msg := &IncomingMessage{Platform: "octo", Content: "@机器人 " + command, Extra: map[string]string{"octo_addressed": "true", "octo_command_text": command}}
	scope := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}}
	req := &qaRequest{msg: msg, scope: scope, channelID: "channel", channel: &IMChannel{TenantID: 1, Platform: "octo", Name: "机器人"}, adapter: &scopeTestAdapter{scope: scope}}
	ctx := (&Service{}).businessContext(context.Background(), req, scope)
	if got := answerevidence.IntentFromContext(ctx); got != answerevidence.IntentNone {
		t.Fatalf("original question intent = %q, want ordinary", got)
	}
	// These rules reach AgentQA through QuotedContext; they must not replace
	// the policy decision made from the normalized native message above.
	ctx = context.WithValue(ctx, executionContextKey{}, "GROUP.md：联系人仅作指引，不主动通知、催办或发布。")
	qa := buildIMQARequest(&types.Session{}, msg.Content, "assistant", "user", nil, nil, nil)
	addExecutionContext(ctx, qa)
	if qa.QuotedContext == "" {
		t.Fatal("GROUP.md was not injected into model context")
	}
	if got := answerevidence.IntentFromContext(answerevidence.WithContract(ctx, qa.Query+qa.QuotedContext)); got != answerevidence.IntentNone {
		t.Fatalf("model context reclassified group question as %q", got)
	}
}
