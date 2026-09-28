package agent

import (
	"context"
	"encoding/json"
	"strings"
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

func TestPreflightRequiresNarrowCitationOnlyAfterSourceRead(t *testing.T) {
	source := answerEvidencePreflight{totalLimit: 1000, evidence: []string{
		"Authorized source read for example/repo:\n" + `{"path":"src/main.go","start_line":1,"end_line":40}`,
	}}
	guidance := source.render()
	require.Contains(t, guidance, "broad preflight source read is for orientation")
	require.Contains(t, guidance, "at most 12 original lines")
	require.Contains(t, guidance, `new current-turn <ref id="wN"/> handle`)
	require.Contains(t, guidance, "Do not handwrite a source_url, GitHub link or line range")

	release := answerEvidencePreflight{totalLimit: 1000, evidence: []string{
		"Official GitHub Release lookup for example/repo:\n" + `{"url":"https://github.com/example/repo/releases/tag/v1"}`,
	}}
	require.NotContains(t, release.render(), "at most 12 original lines", "release-only work must not require a source tool")
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
	// Deliberately do not pre-wrap ctx: this covers direct UI/API agent-chat
	// execution, which previously skipped the Octo-only ingress contract.
	ctx := context.Background()

	state, err := engine.Execute(ctx, "session", "message", "Octo 安卓最新版本更新了什么？", nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, []string{"list", "latest"}, releaseTool.actions())
	require.Len(t, model.calls, 1)
	prompt := systemMessage(t, model.calls[0])
	require.Contains(t, prompt, "https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0")
	require.Contains(t, prompt, "system-retrieved, authorized evidence")
	require.Len(t, state.RoundSteps[0].ToolCalls, 2, "preflight remains auditable in the agent state")
}

func TestCompoundSourceQuestionStillPreflightsNamedRelease(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"r1","repository":"ExampleOrg/octo-android"}],"complete":true}`}
		case "latest":
			return &types.ToolResult{Success: true, Output: `{"repository":"ExampleOrg/octo-android","latest_stable":{"tag_name":"v9.1.0"}}`, Data: map[string]interface{}{
				types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "ds", Repository: "ExampleOrg/octo-android", TagName: "v9.1.0", URL: "https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0", PublishedAt: checkedAt, CheckedAt: checkedAt},
			}}
		default:
			return &types.ToolResult{Success: false, Error: "unexpected action"}
		}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	query := "octo-android 最新版本的源码如何实现？"
	ctx := answerevidence.WithContract(context.Background(), query)
	state := &types.AgentState{}

	evidence := engine.prepareAnswerEvidencePreflight(ctx, state, query)
	require.Equal(t, answerevidence.IntentSource, answerevidence.IntentFromContext(ctx))
	require.Equal(t, []string{"list", "latest"}, releaseTool.actions())
	require.Contains(t, evidence, "v9.1.0")
	require.True(t, answerevidence.ReleaseEvidenceObserved(ctx))
	require.True(t, answerevidence.NeedsEvidenceRetry(ctx, "源码这样实现。"), "the source body is still required")
}

func TestExecutePreservesIngressEvidenceContractWithoutResettingIt(t *testing.T) {
	query := "octo-android 最新版本更新了什么？"
	ctx := answerevidence.WithContract(context.Background(), query)
	answerevidence.RecordReleaseEvidence(ctx, "ExampleOrg/octo-android")
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "正式发布已在入口证据中确认。",
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()

	state, err := engine.Execute(ctx, "session", "message", query, nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, "正式发布已在入口证据中确认。", state.FinalAnswer)
	require.Equal(t, 1, model.callCount, "Execute must retain the ingress state instead of starting a second evidence contract")
	require.True(t, answerevidence.ReleaseEvidenceObserved(ctx))
}

func TestExecuteDoesNotReclassifyOrdinaryQuestionFromGroupRules(t *testing.T) {
	original := "Octo 和 Loop 的关系是什么？"
	ctx := answerevidence.WithContract(context.Background(), original)
	modelQuery := original + "\n\nGROUP.md：联系人仅作指引；版本发布问题请核对资料，不主动通知或催办。"
	require.Equal(t, answerevidence.IntentRelease, answerevidence.Classify(modelQuery))
	answer := "Loop 是 Octo 内的项目与任务协作模块。"
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      answer,
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()

	state, err := engine.Execute(ctx, "session", "message", modelQuery, nil)
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, answer, state.FinalAnswer, "correct relationship answer must be the final answer")
	require.Equal(t, 1, model.callCount, "GROUP.md must not trigger a release nudge or fallback round")
	require.Equal(t, answerevidence.IntentNone, answerevidence.IntentFromContext(ctx))
	require.Len(t, state.RoundSteps, 1, "ordinary answer must not be demoted to an intermediate step")
	require.False(t, state.RoundSteps[0].IntermediateAnswer)
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

func TestAnswerEvidencePreflightKeepsMissingReleaseTargetUnverified(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			if args["query"] == "octo-android" {
				return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"android","repository":"ExampleOrg/octo-android"}],"complete":true}`}
			}
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"web","repository":"ExampleOrg/octo-web"}],"complete":true}`}
		case "latest":
			if args["release_ref"] == "android" {
				return &types.ToolResult{Success: true, Output: `{"repository":"ExampleOrg/octo-android","latest_stable":{"tag_name":"v9.1.0"}}`, Data: map[string]interface{}{
					types.GitHubReleaseLookupDataKey:   types.GitHubReleaseLookupAudit{Repository: "ExampleOrg/octo-android"},
					types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "ds", Repository: "ExampleOrg/octo-android", TagName: "v9.1.0", URL: "https://github.com/ExampleOrg/octo-android/releases/tag/v9.1.0", PublishedAt: checkedAt, CheckedAt: checkedAt},
				}}
			}
			return &types.ToolResult{Success: true, Output: `{"repository":"ExampleOrg/octo-web","no_published_stable_release":true}`, Data: map[string]interface{}{
				types.GitHubReleaseLookupDataKey: types.GitHubReleaseLookupAudit{Repository: "ExampleOrg/octo-web"},
			}}
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	query := "Octo 安卓最新版本、Web 最新版本分别更新了什么？"
	ctx := answerevidence.WithContract(context.Background(), query)
	state := &types.AgentState{}

	evidence := engine.prepareAnswerEvidencePreflight(ctx, state, query)
	require.Equal(t, []string{"list", "latest", "list", "latest"}, releaseTool.actions())
	require.Contains(t, evidence, "ExampleOrg/octo-android")
	require.Equal(t, []string{"exampleorg/octo-android", "exampleorg/octo-web"}, answerevidence.ObservedReleaseLookupRepositories(ctx))
	require.Equal(t, []string{"octo-web"}, answerevidence.MissingReleaseRepositories(ctx))
	require.False(t, answerevidence.ReleaseEvidenceObserved(ctx))
	require.True(t, answerevidence.NeedsEvidenceRetry(ctx, "两个项目均发布了 v9.1.0。"))
	partial := answerevidence.FallbackReply(ctx)
	require.Contains(t, partial, "octo-web")
	require.Contains(t, partial, "v9.1.0")
	require.Contains(t, partial, "[官方 Release](<https://github.com/exampleorg/octo-android/releases/tag/v9.1.0>)")
	require.Contains(t, partial, "未核验更新内容")
}

func TestAnswerEvidencePreflightDoesNotGuessAmbiguousOwnerlessRelease(t *testing.T) {
	latestCalls := 0
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			if args["query"] == "octo-android" {
				return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"a","repository":"Alpha/octo-android"},{"release_ref":"b","repository":"Beta/octo-android"}],"complete":true}`}
			}
			return &types.ToolResult{Success: true, Output: `{"repositories":[],"complete":true}`}
		case "latest":
			latestCalls++
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	query := "Octo 安卓最新版本、Web 最新版本分别是什么？"
	ctx := answerevidence.WithContract(context.Background(), query)
	engine.prepareAnswerEvidencePreflight(ctx, &types.AgentState{}, query)
	require.Zero(t, latestCalls, "neither owner may silently satisfy an ownerless target")
	require.Equal(t, []string{"octo-android", "octo-web"}, answerevidence.MissingReleaseRepositories(ctx))
}

func TestAnswerEvidencePreflightSelectsExplicitReleaseOwner(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)
	selected := ""
	releaseTool := newScriptedPreflightTool(agenttools.ToolGitHubReleaseLookup, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"repositories":[{"release_ref":"wrong-owner","repository":"OtherOrg/octo-web"},{"release_ref":"requested-owner","repository":"ExampleOrg/octo-web"}],"complete":true}`}
		case "latest":
			selected, _ = args["release_ref"].(string)
			return &types.ToolResult{Success: true, Output: `{"repository":"ExampleOrg/octo-web","latest_stable":{"tag_name":"v9.1.0"}}`, Data: map[string]interface{}{
				types.GitHubReleaseLookupDataKey:   types.GitHubReleaseLookupAudit{Repository: "ExampleOrg/octo-web"},
				types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "ds", Repository: "ExampleOrg/octo-web", TagName: "v9.1.0", URL: "https://github.com/ExampleOrg/octo-web/releases/tag/v9.1.0", PublishedAt: checkedAt, CheckedAt: checkedAt},
			}}
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(releaseTool)
	query := "ExampleOrg/octo-web 最新版本是什么？"
	ctx := answerevidence.WithContract(context.Background(), query)

	evidence := engine.prepareAnswerEvidencePreflight(ctx, &types.AgentState{}, query)
	require.Equal(t, []string{"exampleorg/octo-web"}, answerevidence.RequiredReleaseRepositories(ctx))
	require.Equal(t, "requested-owner", selected)
	require.Contains(t, evidence, "ExampleOrg/octo-web")
	require.True(t, answerevidence.ReleaseEvidenceObserved(ctx))
	require.False(t, answerevidence.NeedsEvidenceRetry(ctx, "最新版本是 v9.1.0。"))
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
	actions := sourceTool.actions()
	require.Len(t, actions, 7)
	require.Equal(t, "list", actions[0])
	counts := map[string]int{}
	for _, action := range actions[1:] {
		counts[action]++
	}
	require.Equal(t, map[string]int{"search": 3, "read": 3}, counts, "independent repositories may run in parallel")
	require.Len(t, model.calls, 1)
	prompt := systemMessage(t, model.calls[0])
	for _, repository := range projects {
		require.Contains(t, prompt, repository)
	}
	require.Contains(t, prompt, "do not infer another project's storage")
	require.Len(t, state.RoundSteps[0].ToolCalls, 7)
	active, remaining := answerevidence.PostPreflightSourceBrowseBudget(ctx)
	require.True(t, active)
	require.Equal(t, 10, remaining, "named projects leave room for a complementary repository read")
}

func TestNamedChannelPreflightDoesNotPresentPartialSourceJSONAsEvidence(t *testing.T) {
	const repository = "Mininglamp-OSS/codex-channel-octo"
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s1","repository":"` + repository + `"}],"complete":true}`}
		case "search":
			return &types.ToolResult{Success: true, Output: `{"matches":[{"path":"README.md","line":1}],"complete":true}`}
		case "read":
			body, _ := json.Marshal(map[string]any{"repository": repository, "path": "README.md", "start_line": 1, "end_line": 40, "content": strings.Repeat("source body ", 700), "source_url": "https://github.com/" + repository + "/blob/commit/README.md#L1-L40"})
			return &types.ToolResult{Success: true, Output: string(body)}
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	query := "codex-channel-octo 项目负责什么？"
	ctx := answerevidence.WithContract(context.Background(), query)
	state := &types.AgentState{}
	require.Empty(t, engine.prepareAnswerEvidencePreflight(ctx, state, query))
	require.Len(t, state.RoundSteps, 1)
	read := state.RoundSteps[0].ToolCalls[len(state.RoundSteps[0].ToolCalls)-1]
	require.Equal(t, "read", read.Args["action"])
	require.EqualValues(t, 40, read.Args["end_line"])
	require.False(t, read.Result.Success)
	require.Empty(t, read.Result.Output)
	require.Contains(t, read.Result.Error, "narrower line range")
	require.False(t, answerevidence.IntegrationEvidenceObserved(ctx), "an excerpt hidden from the model cannot satisfy the evidence gate")
}

func TestNamedChannelPreflightDoesNotReadFirstGitignoreMatchAsProjectEvidence(t *testing.T) {
	repository := "Mininglamp-OSS/openclaw-channel-octo"
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			return &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s1","repository":"` + repository + `"}],"complete":true}`}
		case "search":
			return &types.ToolResult{Success: true, Output: `{"matches":[{"path":".gitignore","line":1}],"complete":true}`, Data: map[string]interface{}{
				types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{Repository: repository, Complete: true, Matched: true},
			}}
		case "tree":
			return &types.ToolResult{Success: true, Output: `{"entries":[{"path":".gitignore","directory":false},{"path":"README.md","directory":false}]}`}
		case "read":
			if args["path"] != "README.md" {
				return &types.ToolResult{Success: false, Error: "project overview read an unrelated file"}
			}
			return &types.ToolResult{Success: true, Output: `{"repository":"` + repository + `","path":"README.md","content":"OpenClaw channel plugin for Octo. Connects via WebSocket for real-time messaging."}`, Data: map[string]interface{}{
				types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: repository, Path: "README.md", Revision: "commit"},
			}}
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	query := "Mininglamp-OSS/openclaw-channel-octo 项目负责什么？"
	ctx := answerevidence.WithContract(context.Background(), query)
	state := &types.AgentState{}
	evidence := engine.prepareAnswerEvidencePreflight(ctx, state, query)
	require.Contains(t, evidence, "Connects via WebSocket")
	require.NotContains(t, evidence, ".gitignore")
	require.Equal(t, []string{"list", "search", "tree", "read"}, sourceTool.actions())
	require.True(t, answerevidence.IntegrationEvidenceObserved(ctx))
	active, remaining := answerevidence.PostPreflightSourceBrowseBudget(ctx)
	require.True(t, active)
	require.Equal(t, 6, remaining, "one named adapter must leave room to inspect another repository")
}

func TestPostPreflightBudgetAllowsSecondRepositoryNavigationButRemainsBounded(t *testing.T) {
	require.Equal(t, 6, postPreflightSourceBrowseBudget(1, 0), "list, searches, tree and read must fit for a second repository")
	require.Equal(t, 10, postPreflightSourceBrowseBudget(3, 200000))
	require.Equal(t, 10, postPreflightSourceBrowseBudget(20, 200000), "small windows retain the existing ceiling")
	require.Equal(t, 16, postPreflightSourceBrowseBudget(6, 1000000), "large windows allow a broader named-repository follow-up")
	require.Equal(t, 30, postPreflightSourceBrowseBudget(20, 1000000))
	require.Equal(t, 32, postPreflightSourceBrowseBudget(20, 10000000), "even a very large window cannot create unbounded calls")
}

func TestLargeContextRetainsMoreIndependentRepositoryEvidence(t *testing.T) {
	snippet, total := preflightEvidenceLimits(200000)
	require.Equal(t, answerEvidencePreflightSnippetLimit, snippet)
	require.Equal(t, answerEvidencePreflightTotalLimit, total)

	snippet, total = preflightEvidenceLimits(1000000)
	require.Equal(t, 24000, snippet)
	require.Equal(t, 83333, total)
	preflight := answerEvidencePreflight{snippetLimit: snippet, totalLimit: total}
	for _, label := range []string{"first", "second", "third"} {
		preflight.addEvidence(label, strings.Repeat(label+" ", 5000))
	}
	rendered := preflight.render()
	require.Contains(t, rendered, "third", "later repositories must still reach synthesis")
	require.Greater(t, len(rendered), answerEvidencePreflightTotalLimit, "1M context should carry more than the old cap")
}

func TestNamedChannelImplementationQuestionSkipsOverviewPreflight(t *testing.T) {
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(map[string]interface{}) *types.ToolResult {
		return &types.ToolResult{Success: false, Error: "overview preflight must not run for implementation question"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	query := "openclaw-channel-octo 的源码函数如何实现？"
	ctx := answerevidence.WithContract(context.Background(), query)
	require.True(t, answerevidence.Requires(ctx, answerevidence.IntentSource))
	require.True(t, answerevidence.Requires(ctx, answerevidence.IntentIntegration))
	require.Empty(t, engine.prepareAnswerEvidencePreflight(ctx, &types.AgentState{}, query))
	require.Empty(t, sourceTool.actions())
}

func TestNamedChannelPreflightFindsRepositoryAfterTruncatedCatalog(t *testing.T) {
	const repository = "Mininglamp-OSS/hermes-channel-octo"
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(args map[string]interface{}) *types.ToolResult {
		switch args["action"] {
		case "list":
			if args["query"] == "hermes-channel-octo" {
				return &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s-late","repository":"` + repository + `"}],"complete":true}`}
			}
			return &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s-early","repository":"Other/hermes-channel-octo"}],"complete":false,"next_offset":64}`}
		case "search":
			if args["source_ref"] != "s-late" {
				return &types.ToolResult{Success: false, Error: "searched wrong source reference"}
			}
			return &types.ToolResult{Success: true, Output: `{"matches":[{"path":"README.md","line":1}],"complete":true}`, Data: map[string]interface{}{
				types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{Repository: repository, Complete: true, Matched: true},
			}}
		case "read":
			if args["source_ref"] != "s-late" {
				return &types.ToolResult{Success: false, Error: "read wrong source reference"}
			}
			return &types.ToolResult{Success: true, Output: `{"repository":"` + repository + `","path":"README.md","content":"Hermes channel bridge","source_url":"https://github.com/` + repository + `/blob/commit/README.md#L1"}`, Data: map[string]interface{}{
				types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: repository, Path: "README.md", Revision: "commit"},
			}}
		}
		return &types.ToolResult{Success: false, Error: "unexpected action"}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	query := "hermes-channel-octo 项目是干嘛的？"
	ctx := answerevidence.WithContract(context.Background(), query)
	state := &types.AgentState{}
	evidence := engine.prepareAnswerEvidencePreflight(ctx, state, query)
	require.Contains(t, evidence, "Hermes channel bridge")
	require.Equal(t, []string{"list", "list", "search", "read"}, sourceTool.actions())
	require.Empty(t, answerevidence.MissingRequiredRepositories(ctx))
}

func TestExactSourceReferencesRejectsHiddenOrRepeatedRepositoryLeaves(t *testing.T) {
	const wanted = "hermes-channel-octo"
	require.Empty(t, exactSourceReferences(`{"sources":[{"source_ref":"s1","repository":"One/hermes-channel-octo"}],"complete":false}`, []string{wanted}))
	require.Empty(t, exactSourceReferences(`{"sources":[{"source_ref":"s1","repository":"One/hermes-channel-octo"},{"source_ref":"s2","repository":"Two/hermes-channel-octo"},{"source_ref":"s3","repository":"Three/hermes-channel-octo"}],"complete":true}`, []string{wanted}))
}

func TestExactSourceReferencesMatchesExplicitOwner(t *testing.T) {
	catalog := `{"sources":[{"source_ref":"s1","repository":"One/openclaw-channel-octo"},{"source_ref":"s2","repository":"Two/openclaw-channel-octo"}],"complete":true}`
	require.Equal(t, map[string]string{"two/openclaw-channel-octo": "s2"}, exactSourceReferences(catalog, []string{"two/openclaw-channel-octo"}))
	require.Empty(t, exactSourceReferences(catalog, []string{"openclaw-channel-octo"}), "a bare leaf remains ambiguous")
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
