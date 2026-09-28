package session

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// A pending tool event may precede a streamed answer preamble. The later
// refusal uses the same call ID, but must still retract that late prose from
// the server's persisted answer, not just from the live browser display.
func TestToolRefusalWithSeenCallIDRetractsLateAnswerPreamble(t *testing.T) {
	ctx := context.Background()
	bus := event.NewEventBus()
	streams := &completionEventRecorder{}
	message := &types.Message{ID: "message"}
	h := NewAgentStreamHandler(ctx, "session", "message", "request", 1, time.Now(), message, streams, bus, nil)
	h.Subscribe()
	require.NoError(t, bus.Emit(ctx, event.Event{
		ID: "call-pending", Type: event.EventAgentToolCall,
		Data: event.AgentToolCallData{ToolCallID: "call-1", ToolName: "source_browse"},
	}))
	require.NoError(t, bus.Emit(ctx, event.Event{
		ID: "answer-early", Type: event.EventAgentFinalAnswer,
		Data: event.AgentFinalAnswerData{Content: "unverified preamble"},
	}))
	require.Equal(t, "unverified preamble", h.finalAnswer)
	require.NoError(t, bus.Emit(ctx, event.Event{
		ID: "call-refused", Type: event.EventAgentToolCall,
		Data: event.AgentToolCallData{ToolCallID: "call-1", ToolName: "source_browse"},
	}))
	require.Empty(t, h.finalAnswer)
	require.True(t, h.answerSegments[0].superseded)
	require.NoError(t, bus.Emit(ctx, event.Event{
		ID: "answer-fallback", Type: event.EventAgentFinalAnswer,
		Data: event.AgentFinalAnswerData{Content: "safe fallback", Done: true, IsFallback: true},
	}))
	require.Equal(t, "safe fallback", h.finalAnswer)
	require.True(t, message.IsFallback)
	require.Len(t, streams.events, 4)
	require.Equal(t, types.ResponseTypeToolCall, streams.events[0].Type)
	require.Equal(t, types.ResponseTypeToolCall, streams.events[2].Type)
	require.Equal(t, "call-1", streams.events[2].Data["tool_call_id"])
}
