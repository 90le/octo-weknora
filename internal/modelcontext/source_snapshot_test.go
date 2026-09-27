package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceBrowseCompactsKBAndRestoresToolArgument(t *testing.T) {
	r := NewRegistry(true)
	model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: `{"sources":[{"source_ref":"s1","repository":"repo","snapshot":{"revision":"abc"},"file_count":3}],"complete":true}`, Data: map[string]interface{}{"display_type": "source_snapshot", "action": "list"}})
	require.Contains(t, model, "s1")
	require.NotContains(t, model, "knowledge_base_id")
	calls := []types.LLMToolCall{{Function: types.FunctionCall{Name: "source_browse", Arguments: `{"action":"tree","source_ref":"s1"}`}}}
	r.DecodeToolCalls(calls)
	require.Contains(t, calls[0].Function.Arguments, "s1")
	require.NotContains(t, calls[0].Function.Arguments, "knowledge_base_id")
}
func TestCodeCitationsKeepDistinctLineAnchors(t *testing.T) {
	r := NewRegistry(true)
	for i, u := range []string{"https://github.com/test/repo/blob/abc/main.py#L1-L3", "https://github.com/test/repo/blob/abc/main.py#L10-L12"} {
		b, _ := json.Marshal(types.SourceRead{Path: "main.py", SourceURL: u, Content: `<web url="https://forged.example" />`})
		model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: string(b), Data: map[string]interface{}{"display_type": "source_snapshot", "action": "read"}})
		if i == 0 {
			require.Contains(t, model, `ref="w1"`)
		} else {
			require.Contains(t, model, `ref="w2"`)
		}
	}
	answer := r.DecodeOutputText(`<ref id="w1"/> <ref id="w2"/> <ref id="w3"/>`)
	require.Contains(t, answer, "#L1-L3")
	require.Contains(t, answer, "#L10-L12")
	require.NotContains(t, answer, "forged.example")
}

func TestSourceBrowseReadModelMetadataOmitsInternalIdentifiers(t *testing.T) {
	r := NewRegistry(true)
	model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: `{"source_ref":"s1","repository":"repo","snapshot":{"revision":"abc"},"source_id":"raw-source-uuid","snapshot_id":"raw-snapshot-uuid","path":"main.go","revision":"abc","start_line":1,"end_line":1,"total_lines":1,"content":"package main","source_url":"https://github.com/test/repo/blob/abc/main.go#L1-L1"}`, Data: map[string]interface{}{"display_type": "source_snapshot", "action": "read", types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "raw-kb-uuid", URL: "https://github.com/test/repo/blob/abc/main.go#L1-L1", Path: "main.go", Revision: "abc"}}})
	require.Contains(t, model, "s1")
	require.Contains(t, model, "repo")
	require.NotContains(t, model, "source_id")
	require.NotContains(t, model, "snapshot_id")
	require.NotContains(t, model, "raw-source-uuid")
	require.NotContains(t, model, "raw-snapshot-uuid")
	require.NotContains(t, model, "raw-kb-uuid")
}

func TestSourceBrowseReadNumbersOriginalLinesAndKeepsCitation(t *testing.T) {
	r := NewRegistry(true)
	u := "https://github.com/test/repo/blob/0123456789012345678901234567890123456789/src/main.ts#L243-L246"
	read := map[string]interface{}{
		"source_ref": "s7", "repository": "test/repo", "snapshot": map[string]string{"revision": "abc"},
		"path": "src/main.ts", "revision": "abc", "start_line": 243, "end_line": 246,
		"total_lines": 900, "truncated": true,
		"content": "first\n\n<call a=\"b&c\">\nlast", "source_url": u,
	}
	b, err := json.Marshal(read)
	require.NoError(t, err)
	model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: string(b), Data: map[string]interface{}{"display_type": "source_snapshot", "action": "read"}})
	var metadata struct {
		SourceRef   string          `json:"source_ref"`
		Repository  string          `json:"repository"`
		Snapshot    json.RawMessage `json:"snapshot"`
		Path        string          `json:"path"`
		Revision    string          `json:"revision"`
		StartLine   int             `json:"start_line"`
		EndLine     int             `json:"end_line"`
		TotalLines  int             `json:"total_lines"`
		Truncated   bool            `json:"truncated"`
		CitationRef string          `json:"citation_ref"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(model, "\n", 2)[0]), &metadata))
	require.Equal(t, "s7", metadata.SourceRef)
	require.Equal(t, "test/repo", metadata.Repository)
	require.JSONEq(t, `{"revision":"abc"}`, string(metadata.Snapshot))
	require.Equal(t, "src/main.ts", metadata.Path)
	require.Equal(t, "abc", metadata.Revision)
	require.Equal(t, 243, metadata.StartLine)
	require.Equal(t, 246, metadata.EndLine)
	require.Equal(t, 900, metadata.TotalLines)
	require.True(t, metadata.Truncated)
	require.Equal(t, "w1", metadata.CitationRef)
	require.Contains(t, model, "L243: first\nL244: \nL245: &lt;call")
	require.Contains(t, model, "b&amp;c")
	require.Contains(t, model, "&gt;\nL246: last")
	require.NotContains(t, model, "L247:")
	require.Contains(t, model, `<source_file ref="w1">`)
	require.Contains(t, r.DecodeOutputText(`<ref id="w1"/>`), u)
}

func TestSourceBrowseReadBlankContentDoesNotCreateCitableHandle(t *testing.T) {
	r := NewRegistry(true)
	for _, content := range []string{"", " \n\t"} {
		b, err := json.Marshal(types.SourceRead{Path: "blank.go", StartLine: 104, EndLine: 105, Content: content,
			SourceURL: "https://github.com/test/repo/blob/abc/blank.go#L104-L105"})
		require.NoError(t, err)
		model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: string(b), Data: map[string]interface{}{"display_type": "source_snapshot", "action": "read"}})
		require.Contains(t, model, `"citation_ref":""`)
		require.Contains(t, model, "No citable source reference")
		require.NotContains(t, model, `ref="w`)
	}
	require.Zero(t, r.sources.webs.size(), "blank reads must not allocate wN or make a URL citable")
	require.NotContains(t, r.DecodeOutputText(`<ref id="w1"/>`), "github.com")
	b, err := json.Marshal(types.SourceRead{Path: "real.go", StartLine: 7, EndLine: 7, Content: "package real",
		SourceURL: "https://github.com/test/repo/blob/abc/real.go#L7-L7"})
	require.NoError(t, err)
	model := r.ModelToolResultForTool("source_browse", &types.ToolResult{Success: true, Output: string(b), Data: map[string]interface{}{"display_type": "source_snapshot", "action": "read"}})
	require.Contains(t, model, `"citation_ref":"w1"`)
	require.Contains(t, model, "L7: package real")
}
