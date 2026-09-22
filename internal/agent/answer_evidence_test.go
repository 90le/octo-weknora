package agent

import (
	"context"
	"testing"

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

	releaseCtx := answerevidence.WithContract(context.Background(), "Claude 支持接入 Octo IM 吗？")
	prompt := engine.buildSystemPrompt(releaseCtx)
	require.Contains(t, prompt, "资料未命中")
	require.Contains(t, prompt, "不能作为“不支持”")
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

	releaseCtx := answerevidence.WithContract(context.Background(), "最新版本更新了什么？")
	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolKnowledgeSearch,
		Result: &types.ToolResult{Success: true, Output: `<search_results count="0"></search_results>`},
	}}})
	require.False(t, answerevidence.VerifiedEvidenceObserved(releaseCtx), "a zero-hit RAG call is not release evidence")
	recordAnswerEvidenceFromStep(releaseCtx, types.AgentStep{ToolCalls: []types.ToolCall{{
		Name:   agenttools.ToolKnowledgeSearch,
		Result: &types.ToolResult{Success: true, Output: `<search_results count="1"><chunk>release note</chunk></search_results>`},
	}}})
	require.True(t, answerevidence.VerifiedEvidenceObserved(releaseCtx))
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

func TestExecuteLoopDoesNotPublishUnsupportedReleaseClaimWithoutEvidence(t *testing.T) {
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
	require.Contains(t, state.FinalAnswer, "不能仅因资料未命中")
	require.NotContains(t, state.FinalAnswer, "Claude 不支持")
}

func TestExecuteLoopDeliversSafeUnknownReleaseAnswerWithoutEvidence(t *testing.T) {
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
