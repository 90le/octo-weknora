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

func TestFocusedOctoToolsPreserveSourceMappingAndOpaqueProposalIDs(t *testing.T) {
	for _, name := range []string{"octo_configuration", "octo_contacts", "octo_issues", "octo_knowledge_preview", "octo_knowledge_confirm", "octo_report"} {
		t.Run(name, func(t *testing.T) {
			require.True(t, HasToolPolicy(name))
			r := NewRegistry(true)
			out := r.ModelToolResultForTool(name, &types.ToolResult{Success: true, Output: `{"knowledge_base_id":"actual-kb-uuid","knowledge_id":"actual-doc-uuid","proposal_id":"OP-opaque","issue_id":"issue-opaque"}`})
			require.Contains(t, out, "b1")
			require.Contains(t, out, "d1")
			require.Contains(t, out, "OP-opaque")
			require.Contains(t, out, "issue-opaque")
			if name == "octo_configuration" || name == "octo_knowledge_confirm" {
				return
			}
			calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: name, Arguments: `{"knowledge_base_id":"b1","knowledge_id":"d1","description":"literal b1","content":"literal d1","proposal_id":"OP-opaque"}`}}}
			r.DecodeToolCalls(calls)
			var args map[string]string
			require.NoError(t, json.Unmarshal([]byte(calls[0].Function.Arguments), &args))
			require.Equal(t, "actual-kb-uuid", args["knowledge_base_id"])
			if name == "octo_knowledge_preview" {
				require.Equal(t, "actual-doc-uuid", args["knowledge_id"])
			} else {
				require.Equal(t, "d1", args["knowledge_id"], "undeclared document arguments must not gain a source decoding policy")
			}
			require.Equal(t, "literal b1", args["description"])
			require.Equal(t, "literal d1", args["content"])
			require.Equal(t, "OP-opaque", args["proposal_id"])
		})
	}
}
