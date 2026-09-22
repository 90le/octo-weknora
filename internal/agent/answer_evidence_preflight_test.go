package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type scriptedPreflightTool struct {
	agenttools.BaseTool
	mu      sync.Mutex
	calls   []map[string]interface{}
	handler func(map[string]interface{}) *types.ToolResult
}

func newScriptedPreflightTool(name string, handler func(map[string]interface{}) *types.ToolResult) *scriptedPreflightTool {
	return &scriptedPreflightTool{
		BaseTool: agenttools.NewBaseTool(name, "test preflight tool", json.RawMessage(`{"type":"object"}`)),
		handler:  handler,
	}
}

func (t *scriptedPreflightTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.calls = append(t.calls, args)
	t.mu.Unlock()
	return t.handler(args), nil
}

func (t *scriptedPreflightTool) actions() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.calls))
	for _, call := range t.calls {
		if action, _ := call["action"].(string); action != "" {
			out = append(out, action)
		}
	}
	return out
}

func systemMessage(t *testing.T, call []chat.Message) string {
	t.Helper()
	for _, message := range call {
		if message.Role == "system" {
			return message.Content
		}
	}
	t.Fatal("system message not found")
	return ""
}

func TestAnswerEvidencePreflightReadsAuthorizedLatestReleaseBeforeModel(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"r1","repository":"ExampleOrg/octo-android"}],"complete":true}`}
		case "latest":
			return &types.ToolResult{Success: true, Output: `{"repository":"ExampleOrg/octo-android","latest_stable":{"tag_name":"v9.1.0","url":"https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0"},"checked_at":"2026-09-22T10:00:00Z"}`, Data: map[string]interface{}{
				types.GitHubReleaseLookupDataKey:   types.GitHubReleaseLookupAudit{Repository: "ExampleOrg/octo-android"},
				types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "ds", Repository: "ExampleOrg/octo-android", TagName: "v9.1.0", URL: "https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0", PublishedAt: checkedAt, CheckedAt: checkedAt},
			}}
		default:
			return &types.ToolResult{Success: false, Error: "unexpected action"}
		}
	})
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "正式发布为 v9.1.0。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	ctx := answerevidence.WithContract(context.Background(), "Octo 安卓最新版本更新了什么？")

	state, err := engine.Execute(ctx, "session", "message", "Octo 安卓最新版本更新了什么？", nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.True(t, answerevidence.ReleaseLookupObserved(ctx))
	require.True(t, answerevidence.ReleaseEvidenceObserved(ctx))
	require.Equal(t, []string{"list", "latest"}, releaseTool.actions())
	require.Len(t, model.calls, 1)
	prompt := systemMessage(t, model.calls[0])
	require.Contains(t, prompt, "https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0")
	require.Contains(t, prompt, "system-retrieved, authorized evidence")
	require.Len(t, state.RoundSteps[0].ToolCalls, 2, "preflight remains auditable in the agent state")
}

func TestAnswerEvidencePreflightReadsEveryNamedReleaseCandidate(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	refs := map[string]string{"octo-android": "r1", "octo-web": "r2"}
	repositories := map[string]string{"r1": "ExampleOrg/octo-android", "r2": "ExampleOrg/octo-web"}
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			candidate, _ := args["query"].(string)
			ref := refs[candidate]
			if ref == "" {
				return &types.ToolResult{Success: true, Output: `{"repositories":[],"complete":true}`}
			}
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"` + ref + `","repository":"` + repositories[ref] + `"}],"complete":true}`}
		case "latest":
			ref, _ := args["release_ref"].(string)
			repository := repositories[ref]
			return &types.ToolResult{Success: true, Output: `{"repository":"` + repository + `","latest_stable":{"tag_name":"v9.1.0","url":"https://github.com/` + repository + `/releases/tag/v9.1.0"}}`, Data: map[string]interface{}{
				types.GitHubReleaseLookupDataKey:   types.GitHubReleaseLookupAudit{Repository: repository},
				types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "ds", Repository: repository, TagName: "v9.1.0", URL: "https://github.com/" + repository + "/releases/tag/v9.1.0", PublishedAt: checkedAt, CheckedAt: checkedAt},
			}}
		default:
			return &types.ToolResult{Success: false, Error: "unexpected action"}
		}
	})
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "两个正式发布均已读取。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	query := "octo 安卓最新版本、web 最新版本更新了什么？"
	ctx := answerevidence.WithContract(context.Background(), query)

	_, err := engine.Execute(ctx, "session", "message", query, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"list", "latest", "list", "latest"}, releaseTool.actions())
	prompt := systemMessage(t, model.calls[0])
	require.Contains(t, prompt, "ExampleOrg/octo-android")
	require.Contains(t, prompt, "ExampleOrg/octo-web")
}

func TestAnswerEvidencePreflightSearchesAndReadsEachNamedChannelBeforeModel(t *testing.T) {
	projects := map[string]string{
		"s1": "Mininglamp-OSS/codex-channel-octo",
		"s2": "Mininglamp-OSS/cc-channel-octo",
		"s3": "Mininglamp-OSS/hermes-channel-octo",
	}
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(args map[string]interface{}) *types.ToolResult {
		action, _ := args["action"].(string)
		ref, _ := args["source_ref"].(string)
		switch action {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s1","repository":"Mininglamp-OSS/codex-channel-octo"},{"source_ref":"s2","repository":"Mininglamp-OSS/cc-channel-octo"},{"source_ref":"s3","repository":"Mininglamp-OSS/hermes-channel-octo"}],"complete":true}`}
		case "search":
			return &types.ToolResult{Success: true, Output: `{"source_ref":"` + ref + `","repository":"` + projects[ref] + `","matches":[{"path":"README.md","line":4}],"complete":true}`, Data: map[string]interface{}{
				types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{Repository: projects[ref], Complete: true, Matched: true},
			}}
		case "read":
			repo := projects[ref]
			return &types.ToolResult{Success: true, Output: `{"repository":"` + repo + `","path":"README.md","start_line":1,"end_line":20,"content":"` + repo + ` bridge description","source_url":"https://github.com/` + repo + `/blob/commit/README.md#L1-L20"}`, Data: map[string]interface{}{
				types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: repo, Path: "README.md", Revision: "commit", URL: "https://github.com/" + repo + "/blob/commit/README.md#L1-L20"},
			}}
		default:
			return &types.ToolResult{Success: false, Error: "unexpected action"}
		}
	})
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "三个项目均有各自的桥接说明。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	query := "codex-channel-octo、cc-channel-octo 和 hermes-channel-octo 项目是干嘛的？"
	ctx := answerevidence.WithContract(context.Background(), query)

	state, err := engine.Execute(ctx, "session", "message", query, nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.True(t, answerevidence.SourceSearchObserved(ctx))
	require.True(t, answerevidence.IntegrationEvidenceObserved(ctx))
	require.Empty(t, answerevidence.MissingRequiredRepositories(ctx))
	require.Equal(t, []string{"list", "search", "read", "search", "read", "search", "read"}, sourceTool.actions())
	require.Len(t, model.calls, 1)
	prompt := systemMessage(t, model.calls[0])
	for _, repository := range projects {
		require.Contains(t, prompt, repository)
	}
	require.Contains(t, prompt, "do not infer another project's storage")
	require.Len(t, state.RoundSteps[0].ToolCalls, 7)
}

func TestAnswerEvidencePreflightLeavesAmbiguousReleaseScopeForSafeUnknown(t *testing.T) {
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		if args["action"] == "list" {
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"r1","repository":"One/octo-android"},{"release_ref":"r2","repository":"Two/octo-android"}],"complete":true}`}
		}
		return &types.ToolResult{Success: false, Error: "latest must not be guessed"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	ctx := answerevidence.WithContract(context.Background(), "Octo 安卓最新版本是什么？")
	state := &types.AgentState{}
	evidence := engine.prepareAnswerEvidencePreflight(ctx, state, "Octo 安卓最新版本是什么？")
	require.Empty(t, evidence)
	require.False(t, answerevidence.ReleaseLookupObserved(ctx))
	require.False(t, answerevidence.ReleaseEvidenceObserved(ctx))
	require.Equal(t, []string{"list"}, releaseTool.actions())
}
