package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestAgentCompletionIncludesOnlyCitedKnowledgeReferences(t *testing.T) {
	registry := modelcontext.NewRegistry(true)
	registry.RegisterChunk(modelcontext.ChunkReference{
		ChunkID: "cited-chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", DocumentTitle: "Product guide",
	})
	registry.RegisterChunk(modelcontext.ChunkReference{
		ChunkID: "uncited-chunk", KnowledgeID: "unrelated", KnowledgeBaseID: "kb", DocumentTitle: "Other guide",
	})
	bus := event.NewEventBus()
	var completed event.AgentCompleteData
	bus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		completed = evt.Data.(event.AgentCompleteData)
		return nil
	})
	engine := &AgentEngine{modelContext: registry, eventBus: bus}
	state := &types.AgentState{FinalAnswer: `已核实。<kb doc="Product guide" chunk_id="cited-chunk" kb_id="kb" />`}
	engine.emitCompletionEvent(context.Background(), state, "session", "message", time.Now())

	require.Len(t, completed.KnowledgeRefs, 1)
	ref, ok := completed.KnowledgeRefs[0].(*types.SearchResult)
	require.True(t, ok)
	require.Equal(t, "cited-chunk", ref.ID)
	require.Equal(t, "doc", ref.KnowledgeID)
	require.Equal(t, "kb", ref.KnowledgeBaseID)
}
