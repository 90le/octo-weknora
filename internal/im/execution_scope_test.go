package im

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

type scopeTestAdapter struct {
	Adapter
	scope *ExecutionScope
	err   error
}

func (a *scopeTestAdapter) AuthorizeExecution(context.Context, *IMChannel, *IncomingMessage) (*ExecutionScope, error) {
	return a.scope, a.err
}

func TestOctoExecutionRequiresExplicitScope(t *testing.T) {
	channel := &IMChannel{Platform: "octo"}
	if _, err := authorizeExecution(context.Background(), nil, channel, nil); err == nil {
		t.Fatal("missing authorizer accepted")
	}
	for _, scope := range []*ExecutionScope{nil, {Revision: "1"}, {KnowledgeBaseIDs: []string{"kb"}}, {KnowledgeBaseIDs: []string{""}, Revision: "1"}} {
		if _, err := authorizeExecution(context.Background(), &scopeTestAdapter{scope: scope}, channel, nil); err == nil {
			t.Fatal("empty/widening scope accepted")
		}
	}
	a := &scopeTestAdapter{scope: &ExecutionScope{KnowledgeBaseIDs: []string{"b", "a", "a"}, Revision: "r1"}}
	got, err := authorizeExecution(context.Background(), a, channel, nil)
	if err != nil || len(got.KnowledgeBaseIDs) != 2 || got.KnowledgeBaseIDs[0] != "a" {
		t.Fatal("scope normalization failed")
	}
	a.scope.KnowledgeBaseIDs[0] = "changed"
	if got.KnowledgeBaseIDs[0] != "a" {
		t.Fatal("shared slice exposed")
	}
	other := *got
	other.Revision = "r2"
	if scopeFingerprint(got) == scopeFingerprint(&other) {
		t.Fatal("revision not bound")
	}
	other = *got
	other.AllowPublicWeb = true
	if scopeFingerprint(got) == scopeFingerprint(&other) {
		t.Fatal("public web permission not bound to scope fingerprint")
	}
}

func TestScopedAgentCannotFallBackToAllKnowledge(t *testing.T) {
	original := &types.CustomAgent{Config: types.CustomAgentConfig{KBSelectionMode: "all", KnowledgeBases: []string{"private"}, MCPSelectionMode: "all", SandboxConfigID: "sandbox", SkillsSelectionMode: "all", WebSearchEnabled: true}}
	copy, err := scopeAgent(original, &ExecutionScope{KnowledgeBaseIDs: []string{"public"}, Revision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if copy.Config.KBSelectionMode != "selected" || len(copy.Config.KnowledgeBases) != 1 || copy.Config.KnowledgeBases[0] != "public" {
		t.Fatal("scope not applied")
	}
	if copy.Config.MCPSelectionMode != "none" || copy.Config.SandboxConfigID != "" || copy.Config.WebSearchEnabled {
		t.Fatal("unapproved external capability retained")
	}
	if original.Config.KBSelectionMode != "all" || original.Config.SandboxConfigID != "sandbox" {
		t.Fatal("shared Agent modified")
	}
	if _, err = scopeAgent(nil, &ExecutionScope{KnowledgeBaseIDs: []string{"public"}}); err == nil {
		t.Fatal("missing Agent fell back")
	}
}

func TestOctoScopeDocumentsDoNotPolluteRetrievalQuery(t *testing.T) {
	request := &types.QARequest{Query: "如何安装 CLI？", QuotedContext: "原生引用"}
	ctx := context.WithValue(context.Background(), executionContextKey{}, "GROUP.md: 本群约定")
	addExecutionContext(ctx, request)
	if request.Query != "如何安装 CLI？" {
		t.Fatal("channel documents became search query")
	}
	if request.QuotedContext != "原生引用\n\nGROUP.md: 本群约定" {
		t.Fatal("context lost")
	}
}

func TestScopedAgentPreservesExplicitNativeSourceReader(t *testing.T) {
	agent := &types.CustomAgent{Config: types.CustomAgentConfig{AllowedTools: []string{"source_browse", "shell_exec"}}}
	out, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r"})
	if err != nil || len(out.Config.AllowedTools) != 1 || out.Config.AllowedTools[0] != "source_browse" {
		t.Fatal("native source capability lost or execution widened")
	}
}

func TestScopedAgentPreservesExplicitGitHubReleaseLookup(t *testing.T) {
	agent := &types.CustomAgent{Config: types.CustomAgentConfig{AllowedTools: []string{"github_release_lookup", "shell_exec"}}}
	out, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r"})
	if err != nil || len(out.Config.AllowedTools) != 1 || out.Config.AllowedTools[0] != "github_release_lookup" {
		t.Fatal("native GitHub release lookup lost or execution widened")
	}
}

func TestScopedAgentPublicWebNeedsExactScopeAndAgentEnablement(t *testing.T) {
	agent := &types.CustomAgent{Config: types.CustomAgentConfig{WebSearchEnabled: true}}
	closed, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r"})
	if err != nil || closed.Config.WebSearchEnabled || closed.Config.WebFetchEnabled {
		t.Fatalf("default scoped web was not closed: %+v %v", closed, err)
	}
	for _, tool := range closed.Config.AllowedTools {
		if tool == "web_search" || tool == "web_fetch" {
			t.Fatal("closed scope exposed a web tool")
		}
	}
	opened, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowPublicWeb: true})
	if err != nil || !opened.Config.WebSearchEnabled || !opened.Config.WebFetchEnabled {
		t.Fatalf("authorized scope lacked public search/fetch: %+v %v", opened, err)
	}
	seen := map[string]bool{}
	for _, tool := range opened.Config.AllowedTools {
		seen[tool] = true
	}
	if !seen["web_search"] || !seen["web_fetch"] || seen["shell_exec"] {
		t.Fatalf("unexpected public tool allowlist: %v", opened.Config.AllowedTools)
	}
	if agent.Config.WebFetchEnabled || len(agent.Config.AllowedTools) != 0 {
		t.Fatal("shared Agent was mutated")
	}
	agent.Config.WebSearchEnabled = false
	withoutProvider, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowPublicWeb: true})
	if err != nil || withoutProvider.Config.WebSearchEnabled || withoutProvider.Config.WebFetchEnabled {
		t.Fatalf("scope bypassed original Agent search gate: %+v %v", withoutProvider, err)
	}
	agent.Config.WebSearchEnabled = true
	agent.Config.AllowedTools = []string{"knowledge_search"}
	restricted, err := scopeAgent(agent, &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowPublicWeb: true})
	if err != nil || len(restricted.Config.AllowedTools) != 1 || restricted.Config.AllowedTools[0] != "knowledge_search" {
		t.Fatalf("Agent explicit tool selection widened: %+v %v", restricted, err)
	}
}
