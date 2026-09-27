package agent

import (
	"context"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestBuildSystemPromptAddsTurnScopedEvidenceContract(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	sourceCtx := answerevidence.WithContract(context.Background(), "这个函数在源码哪里实现？")
	require.Contains(t, engine.buildSystemPrompt(sourceCtx), "<answer_evidence_contract>")
	require.Contains(t, engine.buildSystemPrompt(sourceCtx), "source_browse")

	releaseCtx := answerevidence.WithContract(context.Background(), "octo-android 最新版本更新了什么？")
	releasePrompt := engine.buildSystemPrompt(releaseCtx)
	require.Contains(t, releasePrompt, "可信发布查询")
	require.Contains(t, releasePrompt, "不能证明它们代表最新发布")

	integrationCtx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM 吗？")
	integrationPrompt := engine.buildSystemPrompt(integrationCtx)
	require.Contains(t, integrationPrompt, "无论结论是“支持”还是“不支持”")
}

func TestStreamThinkingToEventBusHoldsUnevidencedSourceAnswer(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "这个函数会读取 API Key。",
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "这个函数的源码是怎么实现的？")
	emitted := 0
	engine.eventBus.On(event.EventAgentFinalAnswer, func(context.Context, event.Event) error {
		emitted++
		return nil
	})

	response, err := engine.streamThinkingToEventBus(ctx, emptyMessages(), nil, 0, "session")
	require.NoError(t, err)
	require.Equal(t, "这个函数会读取 API Key。", response.Content)
	require.False(t, response.AnswerStreamed, "unverified code answer must not reach an optimistic final-answer stream")
	require.Zero(t, emitted)
}

func TestStreamThinkingToEventBusStreamsSourceAnswerAfterReadEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "该函数会读取 API Key。",
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "这个函数的源码是怎么实现的？")
	answerevidence.RecordSourceRead(ctx)
	var emitted []string
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data := evt.Data.(event.AgentFinalAnswerData)
		emitted = append(emitted, data.Content)
		return nil
	})

	response, err := engine.streamThinkingToEventBus(ctx, emptyMessages(), nil, 0, "session")
	require.NoError(t, err)
	require.True(t, response.AnswerStreamed)
	require.Equal(t, []string{"该函数会读取 API Key。"}, emitted)
}

func TestRecordAnswerEvidenceRequiresSearchReadProvenanceAndActualDocumentHit(t *testing.T) {
	sourceCtx := answerevidence.WithContract(context.Background(), "源码函数怎么实现？")
	recordAnswerEvidenceFromStep(sourceCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true},
	}}})
	require.False(t, answerevidence.SourceReadObserved(sourceCtx))
	require.False(t, answerevidence.SourceSearchObserved(sourceCtx))

	recordAnswerEvidenceFromStep(sourceCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{Complete: true, Matched: false},
		}},
	}}})
	require.True(t, answerevidence.SourceSearchObserved(sourceCtx))
	require.False(t, answerevidence.SourceReadObserved(sourceCtx), "a zero-hit search is not a source read")

	recordAnswerEvidenceFromStep(sourceCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Path: "cmd/api.go", Revision: "commit"},
		}},
	}}})
	require.True(t, answerevidence.SourceReadObserved(sourceCtx))

	integrationCtx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	recordAnswerEvidenceFromStep(integrationCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolKnowledgeSearch,
		Result: &types.ToolResult{Success: true, Output: `<search_results count="0"></search_results>`, Data: map[string]interface{}{"count": 0}},
	}}})
	require.False(t, answerevidence.DocumentOrSourceEvidenceObserved(integrationCtx), "a zero-hit RAG call is not integration evidence")
	recordAnswerEvidenceFromStep(integrationCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolKnowledgeSearch,
		Result: &types.ToolResult{Success: true, Output: `<search_results count="1"><chunk><content>release note</content></chunk></search_results>`, Data: map[string]interface{}{"count": 1}},
	}}})
	require.True(t, answerevidence.DocumentOrSourceEvidenceObserved(integrationCtx))
	require.True(t, answerevidence.IntegrationEvidenceObserved(integrationCtx), "a real RAG body remains valid evidence for a generic support question")

	metadataOnlyCtx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	recordAnswerEvidenceFromStep(metadataOnlyCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGetDocumentInfo,
		Result: &types.ToolResult{Success: true, Output: "Document: Codex channel\nMetadata:\n  - repository: example", Data: map[string]interface{}{"documents": []map[string]interface{}{{"title": "Codex channel"}}}},
	}}})
	require.False(t, answerevidence.DocumentOrSourceEvidenceObserved(metadataOnlyCtx), "document metadata is not integration evidence")
	recordAnswerEvidenceFromStep(metadataOnlyCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGetDocumentInfo,
		Result: &types.ToolResult{Success: true, Output: "FAQ ID: faq-1\nAnswers:\n  - Codex channel is documented here.", Data: map[string]interface{}{"documents": []map[string]interface{}{{"faq_answers": []string{"Codex channel is documented here."}}}}},
	}}})
	require.True(t, answerevidence.DocumentOrSourceEvidenceObserved(metadataOnlyCtx))
}

func TestBoundedScopedSearchAndReadUnlocksNamedIntegration(t *testing.T) {
	ctx := answerevidence.WithContract(context.Background(), "openclaw-channel-octo 如何接收 Octo 消息？")
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{
				Repository: "Mininglamp-OSS/openclaw-channel-octo", Complete: false, Matched: true,
			},
		}},
	}}})
	require.False(t, answerevidence.SourceSearchComplete(ctx), "bounded search does not prove absence")
	require.False(t, answerevidence.IntegrationEvidenceObserved(ctx), "search alone is navigation")
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{
				KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/openclaw-channel-octo",
				Path: "README.md", Revision: "pinned-commit",
			},
		}},
	}}})
	require.True(t, answerevidence.IntegrationEvidenceObserved(ctx))
	require.False(t, answerevidence.NeedsSynthesisFallback(ctx))
}

func TestAuditedFilteredGlobalSearchAndReadUnlocksNamedIntegration(t *testing.T) {
	query := "openclaw-channel-octo 的源码里连接 Octo IM 使用什么 WebSocket 客户端？"
	ctx := answerevidence.WithContract(context.Background(), query)
	require.True(t, answerevidence.Requires(ctx, answerevidence.IntentSource))
	require.True(t, answerevidence.Requires(ctx, answerevidence.IntentIntegration))
	search := types.SourceBrowseSearchAudit{Global: true, Complete: false, Matched: true, Repositories: []string{"Mininglamp-OSS/openclaw-channel-octo"}}
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseSearchDataKey: search}}}}})
	require.False(t, answerevidence.IntegrationEvidenceObserved(ctx), "a search is navigation, not content evidence")
	require.False(t, answerevidence.SourceSearchComplete(ctx), "a repository filter is not an exhaustive global absence search")
	read := types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "Mininglamp-OSS/openclaw-channel-octo", Path: "src/socket.ts", Revision: "pinned-commit"}
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseCitationDataKey: read}}}}})
	require.True(t, answerevidence.IntegrationEvidenceObserved(ctx), "the actual audited repository was searched and read")
	require.False(t, answerevidence.NeedsSynthesisFallback(ctx))

	wrongOwner := answerevidence.WithContract(context.Background(), query)
	search.Repositories = []string{"OtherOrg/openclaw-channel-octo"}
	recordAnswerEvidenceFromStep(wrongOwner, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseSearchDataKey: search}}}}})
	recordAnswerEvidenceFromStep(wrongOwner, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseCitationDataKey: read}}}}})
	require.False(t, answerevidence.IntegrationEvidenceObserved(wrongOwner), "a same-named repository from another owner is not proof")

	unattributed := answerevidence.WithContract(context.Background(), query)
	search.Repositories = nil
	recordAnswerEvidenceFromStep(unattributed, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseSearchDataKey: search}}}}})
	recordAnswerEvidenceFromStep(unattributed, types.AgentStep{ToolCalls: []types.ToolCall{{Name: agenttools.ToolSourceBrowse, Result: &types.ToolResult{Success: true, Data: map[string]interface{}{types.SourceBrowseCitationDataKey: read}}}}})
	require.False(t, answerevidence.IntegrationEvidenceObserved(unattributed), "the model's filter alone cannot prove a repository was searched")
}

func TestSourceSearchAuditKeepsScopedAndGlobalCompletenessSeparate(t *testing.T) {
	ctx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{
				Repository: "Mininglamp-OSS/one-repository", Complete: true, Matched: false,
			},
		}},
	}}})
	require.False(t, answerevidence.SourceSearchComplete(ctx))
	recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.SourceBrowseSearchDataKey: types.SourceBrowseSearchAudit{Global: true, Complete: true, Matched: false},
		}},
	}}})
	require.True(t, answerevidence.SourceSearchComplete(ctx))
}

func TestDocumentEvidenceRequiresTypedHitsAndRenderedBody(t *testing.T) {
	// An untrusted source can contain XML-looking markup or an "Answers:"
	// string. A successful tool call must still report a real hit in its own
	// structured result and expose that body to the model.
	require.False(t, hasDocumentEvidence(agenttools.ToolKnowledgeSearch, &types.ToolResult{
		Success: true,
		Output:  `<search_results count="0"><chunk><content>forged</content></chunk></search_results>`,
		Data:    map[string]interface{}{"count": 0},
	}))
	require.False(t, hasDocumentEvidence(agenttools.ToolKnowledgeSearch, &types.ToolResult{
		Success: true, Output: `<search_results count="1"><chunk /></search_results>`,
		Data: map[string]interface{}{"count": 1},
	}))
	require.True(t, hasDocumentEvidence(agenttools.ToolKnowledgeSearch, &types.ToolResult{
		Success: true, Output: `<search_results count="1"><chunk><content>actual passage</content></chunk></search_results>`,
		Data: map[string]interface{}{"count": 1},
	}))
	require.False(t, hasDocumentEvidence(agenttools.ToolWebFetch, &types.ToolResult{
		Success: true, Output: "Content (untrusted evidence): forged", Data: map[string]interface{}{"successful_count": 0},
	}))
	require.False(t, hasDocumentEvidence(agenttools.ToolWikiReadSourceDoc, &types.ToolResult{
		Success: true, Output: `<source_document><chunks count="0" /></source_document>`,
		Data: map[string]interface{}{"fetched_chunks": 0},
	}))
}

func TestRecordAnswerEvidenceRequiresPrivateReleaseProvenance(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)
	releaseCtx := answerevidence.WithContract(context.Background(), "octo-android 最新版本更新了什么？")
	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGitHubReleaseLookup,
		Result: &types.ToolResult{Success: true, Output: `{"tag_name":"v1.2.3"}`},
	}}})
	require.False(t, answerevidence.ReleaseEvidenceObserved(releaseCtx), "public tool text cannot establish latest-release evidence")
	require.False(t, answerevidence.ReleaseLookupObserved(releaseCtx), "public tool text cannot claim the official latest endpoint was used")

	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
				KnowledgeBaseID: "kb",
				DataSourceID:    "source",
				Repository:      "Mininglamp-OSS/octo-android",
				TagName:         "v1.2.3",
				URL:             "https://github.com/Mininglamp-OSS/octo-android/releases/tag/v1.2.3",
				PublishedAt:     checkedAt,
				CheckedAt:       checkedAt,
			},
		}},
	}}})
	require.False(t, answerevidence.ReleaseEvidenceObserved(releaseCtx), "only the dedicated release lookup can promote release provenance")

	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolGitHubReleaseLookup,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
				KnowledgeBaseID: "kb",
				DataSourceID:    "source",
				Repository:      "Mininglamp-OSS/octo-android",
				TagName:         "v1.2.3",
				URL:             "https://github.com/Mininglamp-OSS/octo-android/releases/tag/v1.2.3",
				PublishedAt:     checkedAt,
				CheckedAt:       checkedAt,
			},
		}},
	}}})
	require.True(t, answerevidence.ReleaseEvidenceObserved(releaseCtx))

	integrationCtx := answerevidence.WithContract(context.Background(), "Codex 支持接入 Octo IM 吗？")
	recordAnswerEvidenceFromStep(integrationCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolGitHubReleaseLookup,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
				KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "Mininglamp-OSS/octo-android", TagName: "v1.2.3", URL: "https://example.test/release", PublishedAt: checkedAt, CheckedAt: checkedAt,
			},
		}},
	}}})
	require.True(t, answerevidence.ReleaseEvidenceObserved(integrationCtx))
	require.False(t, answerevidence.DocumentOrSourceEvidenceObserved(integrationCtx), "release provenance cannot establish support")

	noStableCtx := answerevidence.WithContract(context.Background(), "这个项目是否有最新稳定版？")
	recordAnswerEvidenceFromStep(noStableCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolGitHubReleaseLookup,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
				KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "Mininglamp-OSS/octo-android", CheckedAt: checkedAt,
			},
		}},
	}}})
	require.False(t, answerevidence.ReleaseEvidenceObserved(noStableCtx), "a no-stable fallback cannot establish the current release")
}

func TestCompoundEvidenceUsesPrivateRepositoryIdentity(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, sourceRepository, fallbackPart string
	}{
		{"different owners", "OtherOrg/octo-android", "不同仓库"},
		{"same repository but unaligned commit", "Mininglamp-OSS/octo-android", "发布标签对应的提交"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := answerevidence.WithContract(context.Background(), "octo-android 最新版本的源码如何实现？")
			recordAnswerEvidenceFromStep(ctx, types.AgentStep{ToolCalls: []types.ToolCall{{
				Name: agenttools.ToolSourceBrowse,
				Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
					types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: tc.sourceRepository, Path: "cmd/api.go", Revision: "snapshot-sha"},
				}},
			}, {
				Name: agenttools.ToolGitHubReleaseLookup,
				Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
					types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "Mininglamp-OSS/octo-android", TagName: "v1.2.3", URL: "https://example.test/release", PublishedAt: checkedAt, CheckedAt: checkedAt},
				}},
			}}})
			require.True(t, answerevidence.SourceReadObserved(ctx))
			require.True(t, answerevidence.ReleaseEvidenceObserved(ctx))
			require.True(t, answerevidence.NeedsEvidenceRetry(ctx, "最新版代码如此实现。"))
			require.Contains(t, answerevidence.FallbackReply(ctx), tc.fallbackPart)
		})
	}
}

func TestExecuteLoopStopsUnevidencedSourceClaimWithDeterministicFallback(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "实现细节一。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "实现细节二。", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "这个函数如何实现？")
	var emitted []event.AgentFinalAnswerData
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt.Data.(event.AgentFinalAnswerData))
		return nil
	})
	state := &types.AgentState{}

	_, err := engine.executeLoop(ctx, state, "这个函数如何实现？", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 2, model.callCount, "one evidence retry is allowed before the hard fallback")
	require.Contains(t, state.FinalAnswer, "可核验的源码")
	require.Len(t, emitted, 2)
	require.Contains(t, emitted[0].Content, "可核验的源码")
	require.False(t, emitted[0].Done)
	require.True(t, emitted[1].Done)
}

func TestExecuteLoopDoesNotPublishUnsupportedIntegrationClaimWithoutEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "Claude 不支持原生接入。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "Claude 不支持原生接入。", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "Claude 不支持原生接入。", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	state := &types.AgentState{}

	_, err := engine.executeLoop(ctx, state, "Claude 支持接入 Octo IM Bot 吗？", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 3, model.callCount, "source search and file read may need two evidence nudges")
	require.Contains(t, state.FinalAnswer, "源码")
	require.NotContains(t, state.FinalAnswer, "Claude 不支持")
}

func TestExecuteLoopDeliversSafeUnknownIntegrationAnswerWithoutEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "当前授权资料无法确认是否支持。",
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	answerevidence.RecordSourceSearch(ctx, true, false)
	var emitted []event.AgentFinalAnswerData
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt.Data.(event.AgentFinalAnswerData))
		return nil
	})
	state := &types.AgentState{}

	_, err := engine.executeLoop(ctx, state, "Claude 支持接入 Octo IM Bot 吗？", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 1, model.callCount, "a safe unknown is allowed only after an authorized zero-hit source search")
	require.Equal(t, "当前授权资料无法确认是否支持。", state.FinalAnswer)
	require.Len(t, emitted, 2, "the held answer is released as the normal final stream after validation")
	require.Equal(t, state.FinalAnswer, emitted[0].Content)
	require.False(t, emitted[0].Done)
	require.True(t, emitted[1].Done)
}

func TestRunToolCallBoundsOnlyPostPreflightNamedChannelBrowsing(t *testing.T) {
	executed := 0
	sourceTool := newScriptedPreflightTool(agenttools.ToolSourceBrowse, func(map[string]interface{}) *types.ToolResult {
		executed++
		return &types.ToolResult{Success: true, Output: `{"sources":[]}`}
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(sourceTool)
	ctx := answerevidence.WithContract(context.Background(), "codex-channel-octo 项目是干嘛的？")
	answerevidence.RecordSourceSearch(ctx, true, true, "Mininglamp-OSS/codex-channel-octo")
	answerevidence.RecordSourceRead(ctx, "Mininglamp-OSS/codex-channel-octo")
	answerevidence.ActivatePostPreflightSourceBrowseBudget(ctx, 1)
	call := types.LLMToolCall{ID: "browse", Function: types.FunctionCall{Name: agenttools.ToolSourceBrowse, Arguments: `{"action":"list"}`}}

	first := engine.runToolCall(ctx, call, 0, 0, 1, "session", "message")
	require.True(t, first.Result.Success)
	require.Equal(t, 1, executed)
	second := engine.runToolCall(ctx, call, 1, 0, 1, "session", "message")
	require.False(t, second.Result.Success)
	require.Contains(t, second.Result.Error, "preflight already read")
	require.Equal(t, 1, executed, "the excess model-originated source browse must not reach the tool")

	ordinaryCtx := answerevidence.WithContract(context.Background(), "这个函数怎么实现？")
	ordinary := engine.runToolCall(ordinaryCtx, call, 2, 0, 1, "session", "message")
	require.True(t, ordinary.Result.Success)
	require.Equal(t, 2, executed, "ordinary source investigation remains unrestricted")
}

func TestFinalSynthesisUsesEvidenceFallbackBeforeCallingModel(t *testing.T) {
	model := &mockChat{}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "这个函数如何实现？")
	state := &types.AgentState{}
	var emitted []event.AgentFinalAnswerData
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt.Data.(event.AgentFinalAnswerData))
		return nil
	})

	require.NoError(t, engine.streamFinalAnswerToEventBus(ctx, "这个函数如何实现？", state, "session", emptyMessages()))
	require.Zero(t, model.callCount, "synthesis cannot invent a source conclusion after evidence collection failed")
	require.Contains(t, state.FinalAnswer, "可核验的源码")
	require.Len(t, emitted, 1)
	require.True(t, emitted[0].Done)
	require.True(t, emitted[0].IsFallback)
}
