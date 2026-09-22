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

func TestRecordAnswerEvidenceRequiresReadProvenanceAndActualDocumentHit(t *testing.T) {
	sourceCtx := answerevidence.WithContract(context.Background(), "源码函数怎么实现？")
	recordAnswerEvidenceFromStep(sourceCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true},
	}}})
	require.False(t, answerevidence.SourceReadObserved(sourceCtx))

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
		Result: &types.ToolResult{Success: true, Output: `<search_results count="0"></search_results>`},
	}}})
	require.False(t, answerevidence.DocumentOrSourceEvidenceObserved(integrationCtx), "a zero-hit RAG call is not integration evidence")
	recordAnswerEvidenceFromStep(integrationCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolKnowledgeSearch,
		Result: &types.ToolResult{Success: true, Output: `<search_results count="1"><chunk>release note</chunk></search_results>`},
	}}})
	require.True(t, answerevidence.DocumentOrSourceEvidenceObserved(integrationCtx))

	metadataOnlyCtx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	recordAnswerEvidenceFromStep(metadataOnlyCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGetDocumentInfo,
		Result: &types.ToolResult{Success: true, Output: "Document: Codex channel\nMetadata:\n  - repository: example"},
	}}})
	require.False(t, answerevidence.DocumentOrSourceEvidenceObserved(metadataOnlyCtx), "document metadata is not integration evidence")
	recordAnswerEvidenceFromStep(metadataOnlyCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGetDocumentInfo,
		Result: &types.ToolResult{Success: true, Output: "FAQ ID: faq-1\nAnswers:\n  - Codex channel is documented here."},
	}}})
	require.True(t, answerevidence.DocumentOrSourceEvidenceObserved(metadataOnlyCtx))
}

func TestRecordAnswerEvidenceRequiresPrivateReleaseProvenance(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)
	releaseCtx := answerevidence.WithContract(context.Background(), "octo-android 最新版本更新了什么？")
	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolGitHubReleaseLookup,
		Result: &types.ToolResult{Success: true, Output: `{"tag_name":"v1.2.3"}`},
	}}})
	require.False(t, answerevidence.ReleaseEvidenceObserved(releaseCtx), "public tool text cannot establish latest-release evidence")

	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name: agenttools.ToolSourceBrowse,
		Result: &types.ToolResult{Success: true, Data: map[string]interface{}{
			types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
				KnowledgeBaseID: "kb",
				DataSourceID:    "source",
				Repository:      "Mininglamp-OSS/octo-android",
				TagName:         "v1.2.3",
				URL:             "https://github.com/Mininglamp-OSS/octo-android/releases/tag/v1.2.3",
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
				KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "Mininglamp-OSS/octo-android", TagName: "v1.2.3", URL: "https://example.test/release", CheckedAt: checkedAt,
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
	require.True(t, answerevidence.ReleaseEvidenceObserved(noStableCtx), "a checked no-stable-release result is valid release provenance")
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
	}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	state := &types.AgentState{}

	_, err := engine.executeLoop(ctx, state, "Claude 支持接入 Octo IM Bot 吗？", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 2, model.callCount)
	require.Contains(t, state.FinalAnswer, "README、接口文档或源码")
	require.NotContains(t, state.FinalAnswer, "Claude 不支持")
}

func TestExecuteLoopDeliversSafeUnknownIntegrationAnswerWithoutEvidence(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "当前授权资料无法确认 Claude 是否支持原生接入。",
		Done:         true,
		FinishReason: "stop",
	}}}}}
	engine := newTestEngine(t, model)
	ctx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM Bot 吗？")
	var emitted []event.AgentFinalAnswerData
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt.Data.(event.AgentFinalAnswerData))
		return nil
	})
	state := &types.AgentState{}

	_, err := engine.executeLoop(ctx, state, "Claude 支持接入 Octo IM Bot 吗？", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 1, model.callCount, "a safe unknown must not be retried or replaced")
	require.Equal(t, "当前授权资料无法确认 Claude 是否支持原生接入。", state.FinalAnswer)
	require.Len(t, emitted, 2, "the held answer is released as the normal final stream after validation")
	require.Equal(t, state.FinalAnswer, emitted[0].Content)
	require.False(t, emitted[0].Done)
	require.True(t, emitted[1].Done)
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
