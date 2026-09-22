package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type sourceToolKB struct {
	interfaces.KnowledgeBaseService
}

func (*sourceToolKB) GetKnowledgeBaseByIDOnly(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
}

type sourceToolCall struct {
	KnowledgeBaseID string
	SourceID        string
	SnapshotID      string
}

type sourceToolReader struct {
	interfaces.SourceSnapshotReader

	mu            sync.Mutex
	summaries     map[string][]types.SourceSummary
	listCalls     []string
	treeCalls     []sourceToolCall
	readCalls     []sourceToolCall
	searches      []sourceToolCall
	results       map[string]*types.SourceSearch
	requireGrant  bool
	omitSourceURL bool
}

func (r *sourceToolReader) ListSourceSnapshots(_ context.Context, kbID string) ([]types.SourceSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls = append(r.listCalls, kbID)
	return append([]types.SourceSummary(nil), r.summaries[kbID]...), nil
}

func (r *sourceToolReader) SourceTree(_ context.Context, kbID, sourceID, snapshotID, _ string, _ int) (*types.SourceTree, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.treeCalls = append(r.treeCalls, sourceToolCall{KnowledgeBaseID: kbID, SourceID: sourceID, SnapshotID: snapshotID})
	return &types.SourceTree{SnapshotID: snapshotID, Entries: []types.SourceTreeEntry{{Path: "src", Directory: true}}, Total: 1}, nil
}

func (r *sourceToolReader) ReadSourceFile(_ context.Context, kbID, sourceID, snapshotID, p string, start, end int) (*types.SourceRead, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readCalls = append(r.readCalls, sourceToolCall{KnowledgeBaseID: kbID, SourceID: sourceID, SnapshotID: snapshotID})
	url := ""
	if !r.omitSourceURL {
		url = "https://github.com/example/repo/blob/commit-1/" + p
	}
	return &types.SourceRead{DataSourceID: sourceID, SnapshotID: snapshotID, Path: p, Revision: "commit-1", StartLine: start, EndLine: end, TotalLines: 12, Content: "source body", SourceURL: url, PreviewURL: "/platform/knowledge-bases/" + kbID + "?source_id=" + sourceID + "&snapshot_id=" + snapshotID}, nil
}

func (r *sourceToolReader) SearchSourceFiles(ctx context.Context, kbID, sourceID, snapshotID, _ string, _ string) (*types.SourceSearch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requireGrant && !access.HasKBGrant(ctx, kbID, 7, types.OrgRoleViewer) {
		return nil, fmt.Errorf("missing KB grant for %s", kbID)
	}
	r.searches = append(r.searches, sourceToolCall{KnowledgeBaseID: kbID, SourceID: sourceID, SnapshotID: snapshotID})
	if result := r.results[sourceID]; result != nil {
		copy := *result
		copy.Matches = append([]types.SourceMatch(nil), result.Matches...)
		return &copy, nil
	}
	return &types.SourceSearch{SnapshotID: snapshotID, Matches: []types.SourceMatch{}, ScannedFiles: 1, Complete: true}, nil
}

func sourceToolContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.UserIDContextKey, "fixture")
}

func sourceSummary(id, snapshotID, repository string) types.SourceSummary {
	return types.SourceSummary{ID: id, Name: repository, Type: "github", Status: "active", SnapshotID: snapshotID, Revision: "commit-1", FileCount: 3}
}

func sourceCatalog(t *testing.T, tool *SourceBrowseTool) sourceBrowseCatalog {
	t.Helper()
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var catalog sourceBrowseCatalog
	require.NoError(t, json.Unmarshal([]byte(result.Output), &catalog))
	return catalog
}

func TestSourceBrowseSchemaExposesOnlySourceRefSelection(t *testing.T) {
	tool := NewSourceBrowseTool(&sourceToolReader{}, &sourceToolKB{}, nil)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	require.Contains(t, schema.Properties, "source_ref")
	require.NotContains(t, schema.Properties, "knowledge_base_id")
	require.NotContains(t, schema.Properties, "source_id")
	require.NotContains(t, schema.Properties, "snapshot_id")
}

func TestSourceBrowseCatalogBindsOpaqueRefAndRedactsInternalIDs(t *testing.T) {
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{
		"kb-uuid": {sourceSummary("source-uuid", "snapshot-uuid", "github.com/example/repo")},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-uuid"}})
	catalog := sourceCatalog(t, tool)
	require.Len(t, catalog.Sources, 1)
	ref := catalog.Sources[0].SourceRef
	require.Equal(t, "s1", ref)
	require.Equal(t, "github.com/example/repo", catalog.Sources[0].Repository)
	require.NotContains(t, mustJSON(t, catalog), "kb-uuid")
	require.NotContains(t, mustJSON(t, catalog), "source-uuid")
	require.NotContains(t, mustJSON(t, catalog), "snapshot-uuid")

	tree, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","source_ref":"s1","path":""}`))
	require.NoError(t, err)
	require.True(t, tree.Success, tree.Error)
	require.Len(t, reader.treeCalls, 1)
	require.Equal(t, sourceToolCall{KnowledgeBaseID: "kb-uuid", SourceID: "source-uuid", SnapshotID: "snapshot-uuid"}, reader.treeCalls[0])
	require.Contains(t, tree.Output, `"source_ref":"s1"`)
	require.NotContains(t, tree.Output, "kb-uuid")
	require.NotContains(t, tree.Output, "source-uuid")
	require.NotContains(t, tree.Output, "snapshot-uuid")

	read, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"s1","path":"src/main.go","start_line":1,"end_line":2}`))
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	require.Len(t, reader.readCalls, 1)
	require.NotContains(t, read.Output, "kb-uuid")
	require.NotContains(t, read.Output, "source-uuid")
	require.NotContains(t, read.Output, "snapshot-uuid")
	citation, ok := read.Data[types.SourceBrowseCitationDataKey].(types.SourceBrowseCitation)
	require.True(t, ok, "read provenance stays in private live ToolResult.Data")
	require.Equal(t, types.SourceBrowseCitation{KnowledgeBaseID: "kb-uuid", Repository: "github.com/example/repo", URL: "https://github.com/example/repo/blob/commit-1/src/main.go", Path: "src/main.go", Revision: "commit-1"}, citation)
	serialized, err := json.Marshal(read)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "kb-uuid", "private provenance must not serialize with a live ToolResult")
}

func TestSourceBrowseReadRetainsPrivateEvidenceForLocalDirectories(t *testing.T) {
	reader := &sourceToolReader{omitSourceURL: true, summaries: map[string][]types.SourceSummary{
		"kb-uuid": {sourceSummary("source-uuid", "snapshot-uuid", "local/project")},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-uuid"}})
	_ = sourceCatalog(t, tool)
	read, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"s1","path":"main.go","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	citation, ok := read.Data[types.SourceBrowseCitationDataKey].(types.SourceBrowseCitation)
	require.True(t, ok, "a local source read still needs private provenance")
	require.Equal(t, "kb-uuid", citation.KnowledgeBaseID)
	require.Equal(t, "local/project", citation.Repository)
	require.Equal(t, "main.go", citation.Path)
	require.Empty(t, citation.URL, "transport may omit a public URL without losing read evidence")
}

func TestSourceBrowseSearchRetainsPrivateAuditWithoutPromotingReadEvidence(t *testing.T) {
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb-uuid": {sourceSummary("source-uuid", "snapshot-uuid", "github.com/example/repo")},
		},
		results: map[string]*types.SourceSearch{
			"source-uuid": {SnapshotID: "snapshot-uuid", Matches: []types.SourceMatch{}, ScannedFiles: 3, Complete: true},
		},
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-uuid"}})
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","query":"missing"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	audit, ok := result.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
	require.True(t, ok)
	require.True(t, audit.Complete)
	require.False(t, audit.Matched)
	_, hasReadCitation := result.Data[types.SourceBrowseCitationDataKey]
	require.False(t, hasReadCitation)
	serialized, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "_source_browse_search")
}

func TestSourceBrowseRejectsMalformedRefsAndSafelyCorrectsLegacyArguments(t *testing.T) {
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{
		"kb-uuid": {sourceSummary("source-uuid", "snapshot-uuid", "github.com/example/repo")},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-uuid"}})

	badRef, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","source_ref":"source-uuid"}`))
	require.NoError(t, err)
	require.False(t, badRef.Success)
	require.Contains(t, badRef.Error, "source_ref")
	require.Empty(t, reader.treeCalls)

	wrongSnapshot, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","knowledge_base_id":"kb-uuid","source_id":"source-uuid","snapshot_id":"wrong-snapshot"}`))
	require.NoError(t, err)
	require.False(t, wrongSnapshot.Success)
	require.Empty(t, reader.treeCalls)

	legacy, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","knowledge_base_id":"kb-uuid","source_id":"source-uuid","snapshot_id":"snapshot-uuid"}`))
	require.NoError(t, err)
	require.True(t, legacy.Success, legacy.Error)
	require.Contains(t, legacy.Output, `"source_ref":"s1"`)
	require.NotContains(t, legacy.Output, "source-uuid")
	require.Len(t, reader.treeCalls, 1)
	require.Equal(t, sourceToolCall{KnowledgeBaseID: "kb-uuid", SourceID: "source-uuid", SnapshotID: "snapshot-uuid"}, reader.treeCalls[0])

	other := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-uuid"}})
	foreignRef, err := other.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","source_ref":"s1"}`))
	require.NoError(t, err)
	require.False(t, foreignRef.Success)
	require.Len(t, reader.treeCalls, 1, "a ref from another tool instance must not resolve")
}

func TestSourceBrowseGlobalSearchCoversAllAuthorizedSnapshotsWithoutScopeWidening(t *testing.T) {
	summaries := make([]types.SourceSummary, 0, 38)
	for i := 0; i < 38; i++ {
		id := fmt.Sprintf("source-%02d", i)
		summaries = append(summaries, sourceSummary(id, fmt.Sprintf("snapshot-%02d", i), fmt.Sprintf("github.com/example/repo-%02d", i)))
	}
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb-authorized": summaries,
			"kb-restricted": {sourceSummary("restricted-source", "restricted-snapshot", "github.com/example/restricted")},
		},
		results: map[string]*types.SourceSearch{
			"source-37": {SnapshotID: "snapshot-37", Matches: []types.SourceMatch{{Path: "late.go", Line: 1, Text: "needle"}}, ScannedFiles: 1, Complete: true},
		},
		requireGrant: true,
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-authorized"},
		{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "kb-restricted", KnowledgeIDs: []string{"one-file"}},
	})
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","query":"needle"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var search sourceBrowseGlobalSearch
	require.NoError(t, json.Unmarshal([]byte(result.Output), &search))
	require.True(t, search.Complete)
	require.Equal(t, 38, search.ScannedSources)
	require.Equal(t, 1, search.MatchedSources)
	require.Len(t, search.Sources, 1)
	require.Equal(t, "s1", search.Sources[0].SourceRef)
	require.Equal(t, "needle", search.Sources[0].Matches[0].Text)
	require.NotContains(t, result.Output, "kb-authorized")
	require.NotContains(t, result.Output, "source-37")
	require.NotContains(t, result.Output, "snapshot-37")
	for _, call := range reader.searches {
		require.Equal(t, "kb-authorized", call.KnowledgeBaseID)
	}
	require.NotContains(t, reader.listCalls, "kb-restricted")
	tree, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"tree","source_ref":"s1"}`))
	require.NoError(t, err)
	require.True(t, tree.Success, tree.Error)
	require.Equal(t, sourceToolCall{KnowledgeBaseID: "kb-authorized", SourceID: "source-37", SnapshotID: "snapshot-37"}, reader.treeCalls[len(reader.treeCalls)-1])
}

func TestSourceToolNeverWidensFileOrTagScope(t *testing.T) {
	ctx := sourceToolContext()
	for _, target := range []*types.SearchTarget{{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "kb", KnowledgeIDs: []string{"file"}}, {Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb", TagIDs: []string{"tag"}}} {
		reader := &sourceToolReader{}
		tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{target})
		result, err := tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"kb"}`))
		require.NoError(t, err)
		require.False(t, result.Success)
		require.Empty(t, reader.listCalls)
	}
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{"kb": {sourceSummary("source", "snapshot", "repo")}}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	result, err := tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"other"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Empty(t, reader.listCalls)
	result, err = tool.Execute(ctx, json.RawMessage(`{"action":"list","knowledge_base_id":"kb"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, []string{"kb"}, reader.listCalls)
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return string(b)
}
