package im

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type scopeTestAdapter struct {
	Adapter
	scope *ExecutionScope
	err   error
}

func TestGeneratedFileDefaultPreservesExistingScopeSerialization(t *testing.T) {
	scope := &ExecutionScope{AccountID: "account", ScopeID: "scope", KnowledgeBaseIDs: []string{"kb"}, Revision: "r"}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ReadIssueKnowledgeBaseIDs":null,"AccountID":"account","ScopeID":"scope",` +
		`"ManageKnowledgeBaseIDs":null,"ReadIssueScopeIDs":null,"CanManageScope":false,` +
		`"AllowKnowledgeCreation":false,"AllowPublicWeb":false,"KnowledgeBaseIDs":["kb"],"Revision":"r"}`
	if string(raw) != want {
		t.Fatalf("disabled capability changed existing session scope: %s", raw)
	}
	fingerprint := ExecutionScopeFingerprint(scope)
	scope.AllowGeneratedFiles = true
	if fingerprint == ExecutionScopeFingerprint(scope) {
		t.Fatal("changing generated-file permission did not invalidate the session scope")
	}
}

type syntheticScopedKB struct {
	interfaces.KnowledgeBaseService
	searched []string
}

func (s *syntheticScopedKB) GetKnowledgeBasesByIDsOnly(
	_ context.Context, ids []string,
) ([]*types.KnowledgeBase, error) {
	kb := &types.KnowledgeBase{ID: ids[0]}
	kb.IndexingStrategy.KeywordEnabled = true
	return []*types.KnowledgeBase{kb}, nil
}

func (s *syntheticScopedKB) ResolveEmbeddingModelKeys(
	context.Context, []*types.KnowledgeBase,
) map[string]string {
	return nil
}

func (s *syntheticScopedKB) HybridSearch(
	_ context.Context, _ string, params types.SearchParams,
) ([]*types.SearchResult, error) {
	s.searched = append([]string(nil), params.KnowledgeBaseIDs...)
	return []*types.SearchResult{{
		ID: "synthetic-chunk", KnowledgeID: "synthetic-document", KnowledgeBaseID: "allowed",
		Content: "SYNTHETIC_IM_SEARCH_MARKER", KnowledgeTitle: "synthetic", Score: 0.9,
	}}, nil
}

func TestScopedKnowledgeSearchReturnsSyntheticCitation(t *testing.T) {
	requested := &types.CustomAgent{Config: types.CustomAgentConfig{
		AllowedTools: []string{tools.ToolSearchKnowledge},
	}}
	scoped, err := scopeAgent(requested, &ExecutionScope{KnowledgeBaseIDs: []string{"allowed"}, Revision: "r"})
	if err != nil || !reflect.DeepEqual(scoped.Config.AllowedTools, []string{tools.ToolSearchKnowledge}) {
		t.Fatalf("scoped search tool not enabled: %+v %v", scoped, err)
	}
	kb := &syntheticScopedKB{}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "allowed"}}
	search := tools.NewSearchKnowledgeTool(kb, nil, nil, targets, nil, nil)
	result, err := search.Execute(context.Background(), json.RawMessage(`{"query":"marker","mode":"keyword"}`))
	if err != nil || result == nil || !result.Success {
		t.Fatalf("synthetic scoped search failed: %+v %v", result, err)
	}
	rows, ok := result.Data["results"].([]map[string]interface{})
	if !ok || len(rows) != 1 || rows[0]["knowledge_base_id"] != "allowed" ||
		rows[0]["knowledge_id"] != "synthetic-document" || rows[0]["chunk_id"] != "synthetic-chunk" {
		t.Fatalf("synthetic citation is missing: %+v", result.Data)
	}
	if !reflect.DeepEqual(kb.searched, []string{"allowed"}) {
		t.Fatalf("search scope changed: %v", kb.searched)
	}
}

func TestScopedKnowledgeToolRejectsUnauthorizedKB(t *testing.T) {
	requested := &types.CustomAgent{Config: types.CustomAgentConfig{AllowedTools: []string{tools.ToolSearchKnowledge}}}
	scoped, err := scopeAgent(requested, &ExecutionScope{KnowledgeBaseIDs: []string{"allowed"}, Revision: "r"})
	if err != nil || !reflect.DeepEqual(
		tools.EffectiveAllowedTools(scoped.Config.AllowedTools), []string{tools.ToolSearchKnowledge},
	) {
		t.Fatalf("scoped search tool not enabled: %+v %v", scoped, err)
	}
	search := tools.NewSearchKnowledgeTool(nil, nil, nil, types.SearchTargets{{KnowledgeBaseID: "allowed"}}, nil, nil)
	result, err := search.Execute(
		context.Background(), json.RawMessage(`{"query":"test","knowledge_base_ids":["forbidden"]}`),
	)
	if err == nil || result == nil || result.Success {
		t.Fatal("search accepted an out-of-scope knowledge base")
	}
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

func TestScopedAgentPreservesConfiguredKnowledgeTools(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		want    []string
	}{
		{"current names", []string{
			tools.ToolSearchKnowledge, tools.ToolReadDocument, tools.ToolListDocuments, "shell_exec",
		}, []string{tools.ToolSearchKnowledge, tools.ToolReadDocument, tools.ToolListDocuments}},
		{"legacy names", []string{
			tools.LegacyToolKnowledgeSearch, tools.LegacyToolGrepChunks,
			tools.LegacyToolGetDocumentInfo, tools.LegacyToolListKnowledgeChunks,
		}, []string{tools.ToolSearchKnowledge, tools.ToolReadDocument}},
		{"explicit restriction", []string{tools.ToolSourceBrowse, "shell_exec"}, []string{tools.ToolSourceBrowse}},
		{"only unsafe", []string{"shell_exec"}, []string{"__no_scoped_tools__"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := &types.CustomAgent{Config: types.CustomAgentConfig{
				KBSelectionMode: "all", AllowedTools: append([]string(nil), tc.allowed...),
			}}
			scope := &ExecutionScope{KnowledgeBaseIDs: []string{"authorized-kb"}, Revision: "r"}
			out, err := scopeAgent(original, scope)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out.Config.AllowedTools, tc.want) {
				t.Fatalf("allowed tools = %v, want %v", out.Config.AllowedTools, tc.want)
			}
			if out.Config.KBSelectionMode != "selected" ||
				!reflect.DeepEqual(out.Config.KnowledgeBases, []string{"authorized-kb"}) {
				t.Fatalf("knowledge scope widened: %+v", out.Config)
			}
			if !reflect.DeepEqual(original.Config.AllowedTools, tc.allowed) ||
				original.Config.KBSelectionMode != "all" {
				t.Fatal("shared Agent configuration was mutated")
			}
		})
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
	if err != nil || len(restricted.Config.AllowedTools) != 1 ||
		restricted.Config.AllowedTools[0] != tools.ToolSearchKnowledge {
		t.Fatalf("Agent explicit tool selection widened: %+v %v", restricted, err)
	}
}

func TestSandboxRequestsRequireIndependentAgentSelection(t *testing.T) {
	scope := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowGeneratedFiles: true}
	original := scopeFingerprint(scope)
	agent := &types.CustomAgent{
		Config: types.CustomAgentConfig{
			SandboxConfigID:     "sandbox",
			SkillsSelectionMode: "selected",
			SelectedSkills:      []string{"allowed"},
		},
	}
	for _, tc := range []struct {
		name          string
		shell, skills bool
		wantMode      string
	}{
		{"legacy markdown", false, false, "none"},
		{"shell only", true, false, "none"},
		{"skills only", false, true, "selected"},
		{"both", true, true, "selected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := *scope
			next.AllowSandboxShell = tc.shell
			next.AllowSandboxSkills = tc.skills
			if (tc.shell || tc.skills) && scopeFingerprint(&next) == original {
				t.Fatal("switch absent from fingerprint")
			}
			got, err := scopeAgent(agent, &next)
			if err != nil {
				t.Fatal(err)
			}
			wantID := ""
			if tc.shell || tc.skills {
				wantID = "sandbox"
			}
			if got.Config.SandboxConfigID != wantID || got.Config.SkillsSelectionMode != tc.wantMode {
				t.Fatalf("unexpected scope: %+v", got.Config)
			}
			if tc.skills {
				got.Config.SelectedSkills[0] = "other"
				if agent.Config.SelectedSkills[0] != "allowed" {
					t.Fatal("selection shared")
				}
			}
		})
	}
	host := *agent
	host.Config.SandboxConfigID = "host"
	got, err := scopeAgent(
		&host,
		&ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowSandboxShell: true},
	)
	if err != nil || got.Config.SandboxConfigID != "" {
		t.Fatal("host config admitted")
	}
}

func TestFileDeliveryScopeFingerprintIsIndependentOfLegacyMarkdown(t *testing.T) {
	legacy := &ExecutionScope{KnowledgeBaseIDs: []string{"kb"}, Revision: "r", AllowGeneratedFiles: true}
	if legacy.AllowFileDelivery || legacy.AllowSandboxShell || legacy.AllowSandboxSkills || legacy.AllowPublicWeb {
		t.Fatal("legacy permission widened")
	}
	newer := *legacy
	newer.AllowFileDelivery = true
	if scopeFingerprint(&newer) == scopeFingerprint(legacy) {
		t.Fatal("generic file delivery not bound to scope")
	}
	newer.AllowGeneratedFiles = false
	if !newer.AllowFileDelivery {
		t.Fatal("generic permission depends on Markdown")
	}
}
