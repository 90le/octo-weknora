package modelcontext

import (
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOctoBusinessRestoresNativeKnowledgeHandles(t *testing.T) {
	r := NewRegistry(true)
	out := r.ModelToolResultForTool("octo_knowledge_operations", &types.ToolResult{Success: true, Output: `{"knowledge_base_id":"actual-kb-uuid","knowledge_id":"actual-doc-uuid","proposal_id":"OP-opaque","issue_id":"KI-opaque"}`})
	require.Contains(t, out, "b1")
	require.Contains(t, out, "d1")
	require.Contains(t, out, "OP-opaque")
	require.Contains(t, out, "KI-opaque")
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: "octo_knowledge_operations", Arguments: `{"operation":"propose","knowledge_base_id":"b1","knowledge_id":"d1","content":"literal b1 and d1","proposal_id":"OP-opaque"}`}}}
	r.DecodeToolCalls(calls)
	var args map[string]string
	require.NoError(t, json.Unmarshal([]byte(calls[0].Function.Arguments), &args))
	require.Equal(t, "actual-kb-uuid", args["knowledge_base_id"])
	require.Equal(t, "actual-doc-uuid", args["knowledge_id"])
	require.Equal(t, "literal b1 and d1", args["content"])
	require.Equal(t, "OP-opaque", args["proposal_id"])
}
