package modelcontext

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitedKnowledgeReferencesRequiresCurrentEvidenceAndExactFinalCitation(t *testing.T) {
	r := NewRegistry(true)
	r.RegisterContextChunk(ChunkReference{ChunkID: "history", KnowledgeID: "old-doc", KnowledgeBaseID: "kb", DocumentTitle: "Old"})
	r.RegisterChunk(ChunkReference{ChunkID: "used", KnowledgeID: "doc", KnowledgeBaseID: "kb", DocumentTitle: "Actual README", ChunkIndex: 3})
	r.RegisterChunk(ChunkReference{ChunkID: "unused", KnowledgeID: "other-doc", KnowledgeBaseID: "kb", DocumentTitle: "Unrelated"})
	answer := `事实。<kb doc="forged title" chunk_id="used" kb_id="kb" /> 再次引用。<kb chunk_id="used" /> ` +
		`历史引用。<kb chunk_id="history" /> 其他库。<kb chunk_id="unused" kb_id="secret" /> ` +
		`伪造。<kb chunk_id="invented" />`
	refs := r.CitedKnowledgeReferences(answer)
	require.Len(t, refs, 1)
	require.Equal(t, "used", refs[0].ID)
	require.Equal(t, "doc", refs[0].KnowledgeID)
	require.Equal(t, "kb", refs[0].KnowledgeBaseID)
	require.Equal(t, "Actual README", refs[0].KnowledgeTitle)
	require.Equal(t, 3, refs[0].ChunkIndex)

	require.Empty(t, NewRegistry(false).CitedKnowledgeReferences(answer))
	require.Empty(t, (*Registry)(nil).CitedKnowledgeReferences(answer))
}

func TestCitedKnowledgeReferencesFromRealSearchAndWikiToolResultShapes(t *testing.T) {
	for _, tc := range []struct {
		name, tool, displayType, resultKey string
		row                                map[string]interface{}
	}{
		{"RAG search", "knowledge_search", "search_results", "results", map[string]interface{}{
			"chunk_id": "rag-chunk", "knowledge_id": "rag-doc", "knowledge_base_id": "kb", "knowledge_title": "Product README", "content": "RAG evidence",
		}},
		{"Wiki source document", "wiki_read_source_doc", "knowledge_chunks_list", "chunks", map[string]interface{}{
			"chunk_id": "wiki-chunk", "knowledge_id": "wiki-doc", "knowledge_base": "kb", "knowledge_title": "Source document", "content": "Wiki source evidence",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(true)
			modelText := r.ModelToolResultForTool(tc.tool, &types.ToolResult{Success: true, Data: map[string]interface{}{
				"display_type": tc.displayType,
				tc.resultKey:   []map[string]interface{}{tc.row},
			}})
			require.Contains(t, modelText, `id="c1"`)
			answer := r.DecodeOutputText(`Answer. <ref id="c1"/>`)
			refs := r.CitedKnowledgeReferences(answer)
			require.Len(t, refs, 1)
			require.Equal(t, tc.row["chunk_id"], refs[0].ID)
			require.Equal(t, tc.row["knowledge_id"], refs[0].KnowledgeID)
			require.Equal(t, "kb", refs[0].KnowledgeBaseID)
		})
	}
}
