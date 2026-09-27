package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestSearchAuditDistinguishesScopedFromGlobalCompleteness(t *testing.T) {
	scoped := searchAuditFromOutput(sourceBrowseSearch{Repository: "Mininglamp-OSS/repo", Complete: true})
	require.NotNil(t, scoped)
	require.False(t, scoped.Global)
	require.Equal(t, "Mininglamp-OSS/repo", scoped.Repository)

	global := searchAuditFromOutput(sourceBrowseGlobalSearch{Complete: true})
	require.NotNil(t, global)
	require.True(t, global.Global)
	require.Empty(t, global.Repository)
}

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
	readErr       error
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
	if r.readErr != nil {
		return nil, r.readErr
	}
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
	return types.SourceSummary{ID: id, Name: repository, Repository: repository, Type: "github", Status: "active", SnapshotID: snapshotID, Revision: "commit-1", FileCount: 3}
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

func TestSourceBrowseUsesConfiguredGitHubIdentityInsteadOfDisplayName(t *testing.T) {
	summary := sourceSummary("source", "snapshot", "Friendly custom label")
	summary.Repository = "Mininglamp-OSS/openclaw-channel-octo"
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{"kb": {summary}}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	listed, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list","repository_query":"Mininglamp-OSS/openclaw-channel-octo"}`))
	require.NoError(t, err)
	require.True(t, listed.Success, listed.Error)
	var catalog sourceBrowseCatalog
	require.NoError(t, json.Unmarshal([]byte(listed.Output), &catalog))
	require.Len(t, catalog.Sources, 1, "canonical identity must be searchable even with a custom display name")
	require.Equal(t, summary.Repository, catalog.Sources[0].Repository)
	ref := catalog.Sources[0].SourceRef
	searched, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","source_ref":"`+ref+`","query":"message"}`))
	require.NoError(t, err)
	require.True(t, searched.Success, searched.Error)
	audit, ok := searched.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
	require.True(t, ok)
	require.Equal(t, summary.Repository, audit.Repository)
	read, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"`+ref+`","path":"README.md","start_line":1,"end_line":2}`))
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	citation, ok := read.Data[types.SourceBrowseCitationDataKey].(types.SourceBrowseCitation)
	require.True(t, ok)
	require.Equal(t, summary.Repository, citation.Repository)

	local := bindingFromSummary("kb", 7, types.SourceSummary{Name: "Friendly local folder", Type: "local_folder"})
	require.Equal(t, "Friendly local folder", local.Repository)

	invalid := sourceSummary("bad", "old-snapshot", "OtherOrg/openclaw-channel-octo")
	invalid.Repository = ""
	require.Empty(t, bindingFromSummary("kb", 7, invalid).Repository, "an editable GitHub name is not provenance")
	badTool := NewSourceBrowseTool(&sourceToolReader{summaries: map[string][]types.SourceSummary{"kb": {invalid}}},
		&sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	badCatalog := sourceCatalog(t, badTool)
	require.Empty(t, badCatalog.Sources)
	require.False(t, badCatalog.Complete, "an unidentifiable GitHub snapshot keeps exhaustive search incomplete")
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

func TestSourceBrowseCatalogFindsNamedRepositoryBeyondFirstPage(t *testing.T) {
	summaries := make([]types.SourceSummary, 0, 101)
	for i := 0; i < 100; i++ {
		summaries = append(summaries, sourceSummary(fmt.Sprintf("source-%03d", i), fmt.Sprintf("snapshot-%03d", i), fmt.Sprintf("github.com/example/repo-%03d", i)))
	}
	summaries = append(summaries, sourceSummary("source-target", "snapshot-target", "github.com/example/target-channel-octo"))
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{
		"kb":         summaries,
		"restricted": {sourceSummary("other", "other-snapshot", "github.com/example/target-channel-octo")},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"},
		{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "restricted", KnowledgeIDs: []string{"one-file"}},
	})
	first := sourceCatalog(t, tool)
	require.Len(t, first.Sources, maxSourceBrowseCatalogEntries)
	require.False(t, first.Complete)
	require.NotNil(t, first.NextOffset)
	require.Equal(t, maxSourceBrowseCatalogEntries, *first.NextOffset)

	page, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list","offset":64}`))
	require.NoError(t, err)
	require.True(t, page.Success, page.Error)
	var second sourceBrowseCatalog
	require.NoError(t, json.Unmarshal([]byte(page.Output), &second))
	require.Len(t, second.Sources, 37)
	require.True(t, second.Complete)
	require.Nil(t, second.NextOffset)

	filtered, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list","query":"TARGET-CHANNEL-OCTO"}`))
	require.NoError(t, err)
	require.True(t, filtered.Success, filtered.Error)
	var selected sourceBrowseCatalog
	require.NoError(t, json.Unmarshal([]byte(filtered.Output), &selected))
	require.True(t, selected.Complete)
	require.Len(t, selected.Sources, 1)
	require.Equal(t, "github.com/example/target-channel-octo", selected.Sources[0].Repository)
	require.NotContains(t, filtered.Output, "restricted")
	require.NotContains(t, reader.listCalls, "restricted")

	// A live Octo turn used repository_query + limit with action=list. These
	// selectors narrow the same authorized catalog; rejecting them stranded
	// the agent before it could obtain a source_ref.
	alias, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list","repository_query":"TARGET-CHANNEL-OCTO","limit":64}`))
	require.NoError(t, err)
	require.True(t, alias.Success, alias.Error)
	var selectedByAlias sourceBrowseCatalog
	require.NoError(t, json.Unmarshal([]byte(alias.Output), &selectedByAlias))
	require.Len(t, selectedByAlias.Sources, 1)
	require.Equal(t, "github.com/example/target-channel-octo", selectedByAlias.Sources[0].Repository)
	require.NotContains(t, alias.Output, "restricted")

	conflict, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"list","query":"octo-cli","repository_query":"different"}`))
	require.NoError(t, err)
	require.False(t, conflict.Success)
	require.Contains(t, conflict.Error, "must refer to the same repository")

	read, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"`+selected.Sources[0].SourceRef+`","path":"README.md","start_line":1,"end_line":2}`))
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	require.Equal(t, "source-target", reader.readCalls[len(reader.readCalls)-1].SourceID)
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

func TestSourceBrowseGlobalSearchSelectsNamedRepositoryBeyondFirstPage(t *testing.T) {
	summaries := make([]types.SourceSummary, 0, 121)
	for i := 0; i < 120; i++ {
		summaries = append(summaries, sourceSummary(fmt.Sprintf("source-%03d", i), fmt.Sprintf("snapshot-%03d", i), fmt.Sprintf("github.com/example/repo-%03d", i)))
	}
	summaries = append(summaries, sourceSummary("source-target", "snapshot-target", "github.com/example/target-channel-octo"))
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb-authorized": summaries,
			"kb-restricted": {sourceSummary("source-restricted", "snapshot-restricted", "github.com/example/target-channel-octo")},
		},
		results: map[string]*types.SourceSearch{
			"source-target": {SnapshotID: "snapshot-target", Matches: []types.SourceMatch{{Path: "README.md", Line: 4, Text: "bridge fact"}}, ScannedFiles: 1, Complete: true},
		},
		requireGrant: true,
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb-authorized"},
		{Type: types.SearchTargetTypeKnowledge, TenantID: 7, KnowledgeBaseID: "kb-restricted", KnowledgeIDs: []string{"one-file"}},
	})
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","query":"bridge fact","repository_query":"TARGET-CHANNEL-OCTO"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var search sourceBrowseGlobalSearch
	require.NoError(t, json.Unmarshal([]byte(result.Output), &search))
	require.Equal(t, "TARGET-CHANNEL-OCTO", search.RepositoryQuery)
	require.False(t, search.Complete, "a named repository search is not an exhaustive all-source search")
	require.Nil(t, search.NextOffset)
	require.Equal(t, 1, search.ScannedSources)
	require.Equal(t, 1, search.MatchedSources)
	require.Len(t, search.Sources, 1)
	require.Equal(t, "github.com/example/target-channel-octo", search.Sources[0].Repository)
	require.Equal(t, "bridge fact", search.Sources[0].Matches[0].Text)
	require.Equal(t, []sourceToolCall{{KnowledgeBaseID: "kb-authorized", SourceID: "source-target", SnapshotID: "snapshot-target"}}, reader.searches)
	require.NotContains(t, reader.listCalls, "kb-restricted")
	require.NotContains(t, result.Output, "kb-authorized")
	require.NotContains(t, result.Output, "source-target")
	require.NotContains(t, result.Output, "snapshot-target")
	audit, ok := result.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
	require.True(t, ok)
	require.False(t, audit.Complete)
	require.True(t, audit.Matched)

	read, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"`+search.Sources[0].SourceRef+`","path":"README.md","start_line":1,"end_line":6}`))
	require.NoError(t, err)
	require.True(t, read.Success, read.Error)
	require.Contains(t, read.Output, "https://github.com/example/repo/blob/commit-1/README.md")
	require.Equal(t, "source-target", reader.readCalls[len(reader.readCalls)-1].SourceID)
}

func TestSourceBrowseGlobalSearchPagesAuthorizedSourcesWithoutClaimingFullCoverage(t *testing.T) {
	summaries := make([]types.SourceSummary, 0, 120)
	for i := 0; i < 120; i++ {
		summaries = append(summaries, sourceSummary(fmt.Sprintf("source-%03d", i), fmt.Sprintf("snapshot-%03d", i), fmt.Sprintf("github.com/example/repo-%03d", i)))
	}
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{"kb": summaries}, results: map[string]*types.SourceSearch{
		"source-100": {SnapshotID: "snapshot-100", Matches: []types.SourceMatch{{Path: "late.go", Line: 7, Text: "needle"}}, ScannedFiles: 1, Complete: true},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	for _, tc := range []struct {
		offset      int
		wantNext    int
		wantMatches int
	}{
		{offset: 0, wantNext: 16},
		{offset: 96, wantNext: 112, wantMatches: 1},
		{offset: 112, wantNext: 0},
	} {
		result, err := tool.Execute(sourceToolContext(), json.RawMessage(fmt.Sprintf(`{"action":"search","query":"needle","offset":%d,"limit":16}`, tc.offset)))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		var search sourceBrowseGlobalSearch
		require.NoError(t, json.Unmarshal([]byte(result.Output), &search))
		require.Equal(t, tc.offset, search.Offset)
		require.False(t, search.Complete, "a partial page cannot prove a source fact is absent")
		require.Equal(t, tc.wantMatches, search.MatchedSources)
		if tc.wantNext > 0 {
			require.NotNil(t, search.NextOffset)
			require.Equal(t, tc.wantNext, *search.NextOffset)
		} else {
			require.Nil(t, search.NextOffset)
		}
		audit, ok := result.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
		require.True(t, ok)
		require.False(t, audit.Complete)
	}
}

func TestSourceBrowseGlobalSearchRejectsUnsafePageSelectors(t *testing.T) {
	reader := &sourceToolReader{}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	for _, args := range []string{
		`{"action":"search","query":"needle","offset":-1}`,
		`{"action":"search","query":"needle","limit":65}`,
		`{"action":"search","query":"needle","repository_query":"` + strings.Repeat("x", 129) + `"}`,
		`{"action":"search","source_ref":"s1","query":"needle","repository_query":"repo"}`,
		`{"action":"search","source_ref":"s1","query":"needle","offset":1}`,
		`{"action":"tree","source_ref":"s1","repository_query":"repo"}`,
		`{"action":"read","source_ref":"s1","path":"README.md","limit":1}`,
	} {
		result, err := tool.Execute(sourceToolContext(), json.RawMessage(args))
		require.NoError(t, err)
		require.False(t, result.Success, args)
	}
	require.Empty(t, reader.listCalls)
	require.Empty(t, reader.searches)
}

func TestSourceBrowseGlobalSearchEmptyRepositoryFilterIsNotExhaustive(t *testing.T) {
	reader := &sourceToolReader{summaries: map[string][]types.SourceSummary{
		"kb": {sourceSummary("source", "snapshot", "github.com/example/known")},
	}}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","query":"needle","repository_query":"missing"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var search sourceBrowseGlobalSearch
	require.NoError(t, json.Unmarshal([]byte(result.Output), &search))
	require.Equal(t, "missing", search.RepositoryQuery)
	require.False(t, search.Complete)
	require.Zero(t, search.ScannedSources)
	require.Empty(t, reader.searches)
	audit, ok := result.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
	require.True(t, ok)
	require.False(t, audit.Complete)
}

func TestSourceBrowseGlobalSearchMarksConfiguredUnsyncedSourceIncomplete(t *testing.T) {
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb": {
				sourceSummary("ready", "snapshot-ready", "github.com/example/ready"),
				{ID: "sync-required", Name: "github.com/example/pending", Type: "github", Status: "sync_required"},
			},
		},
		results: map[string]*types.SourceSearch{
			"ready": {SnapshotID: "snapshot-ready", Matches: []types.SourceMatch{}, ScannedFiles: 3, Complete: true},
		},
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"search","query":"missing"}`))
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	var search sourceBrowseGlobalSearch
	require.NoError(t, json.Unmarshal([]byte(result.Output), &search))
	require.False(t, search.Complete, "an unsynced source cannot prove an exhaustive zero-hit search")
	audit, ok := result.Data[types.SourceBrowseSearchDataKey].(types.SourceBrowseSearchAudit)
	require.True(t, ok)
	require.False(t, audit.Complete)
}

func TestSourceBrowseReadExplainsUnreadableSnapshotWithoutSuggestingRefRetry(t *testing.T) {
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb": {sourceSummary("source", "snapshot", "github.com/example/repo")},
		},
		readErr: snapshot.ErrUnavailable,
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	_ = sourceCatalog(t, tool)
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"s1","path":"main.go","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not currently readable")
	require.Contains(t, result.Error, "source sync")
	require.NotContains(t, result.Error, "copy a returned source_ref")
}

func TestSourceBrowseReadExplainsMissingPathWithoutSuggestingRefRetry(t *testing.T) {
	reader := &sourceToolReader{
		summaries: map[string][]types.SourceSummary{
			"kb": {sourceSummary("source", "snapshot", "github.com/example/repo")},
		},
		readErr: access.ErrNotFound,
	}
	tool := NewSourceBrowseTool(reader, &sourceToolKB{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, TenantID: 7, KnowledgeBaseID: "kb"}})
	_ = sourceCatalog(t, tool)
	result, err := tool.Execute(sourceToolContext(), json.RawMessage(`{"action":"read","source_ref":"s1","path":"deleted.go","start_line":1,"end_line":1}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "path is unavailable")
	require.Contains(t, result.Error, "tree or search")
	require.NotContains(t, result.Error, "copy a returned source_ref")
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
