package im

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type octoCitationKnowledge struct {
	interfaces.KnowledgeService
	rows      map[string]*types.Knowledge
	requested []string
}

func (f *octoCitationKnowledge) GetKnowledgeByID(_ context.Context, id string) (*types.Knowledge, error) {
	f.requested = append(f.requested, id)
	row := f.rows[id]
	if row == nil {
		return nil, errors.New("not found")
	}
	return row, nil
}
func octoCitationContext() context.Context {
	p := octobusiness.Principal{TenantID: 1, AccountID: "bot", ChannelID: "group", ScopeID: "scope", UserID: "native", KnowledgeBaseIDs: []string{"kb"}}
	fresh := p
	p.Validate = func(context.Context) (octobusiness.Principal, error) { return fresh, nil }
	return octobusiness.WithPrincipal(context.Background(), p)
}
func TestOctoCitationsOnlyLinkExplicitlyUsedAuthorizedReferences(t *testing.T) {
	commit := strings.Repeat("a", 40)
	link := "https://github.com/test/project/blob/" + commit + "/README.md"
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"used":   {ID: "used", TenantID: 1, KnowledgeBaseID: "kb", Title: "Octo CLI README", Source: link, EnableStatus: "enabled"},
		"unused": {ID: "unused", TenantID: 1, KnowledgeBaseID: "kb", Title: "Unrelated documentation", Source: "https://example.org/irrelevant", EnableStatus: "enabled"},
	}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{{ID: "chunk-used", KnowledgeID: "used", KnowledgeBaseID: "kb", KnowledgeTitle: "Octo CLI README"}, {ID: "chunk-unused", KnowledgeID: "unused", KnowledgeBaseID: "kb", KnowledgeTitle: "Unrelated documentation"}}
	answer := service.appendOctoSources(octoCitationContext(), `安装 npm 包。<kb doc="Octo CLI README" chunk_id="chunk-used" kb_id="kb"/>`, refs)
	require.Contains(t, answer, link)
	require.NotContains(t, answer, "irrelevant")
	require.Equal(t, []string{"used"}, knowledge.requested)
	require.Contains(t, stripIMCitationTags(answer), "README.md")
	unchanged := service.appendOctoSources(context.Background(), `answer <kb chunk_id="chunk-used"/>`, refs)
	require.NotContains(t, unchanged, link)
}
func TestOctoPlainSourceTitleMustBeUniqueAndExplicit(t *testing.T) {
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{"one": {ID: "one", TenantID: 1, KnowledgeBaseID: "kb", Title: "Octo CLI README", EnableStatus: "enabled", Source: "https://example.org/guide"}}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{{ID: "c1", KnowledgeID: "one", KnowledgeBaseID: "kb", KnowledgeTitle: "Octo CLI README"}}
	answer := service.appendOctoSources(octoCitationContext(), "请查看 Octo CLI README。", refs)
	require.NotContains(t, answer, "https://")
	answer = service.appendOctoSources(octoCitationContext(), "安装命令。\n来源：Octo CLI README", refs)
	require.Contains(t, answer, "https://example.org/guide")
	refs = append(refs, &types.SearchResult{ID: "c2", KnowledgeID: "two", KnowledgeBaseID: "other", KnowledgeTitle: "Octo CLI README"})
	answer = service.appendOctoSources(octoCitationContext(), "安装命令。\n来源：Octo CLI README", refs)
	require.NotContains(t, answer, "https://", "ambiguous title cannot choose a different KB")
}
func TestOctoCitationsRejectDraftsWrongKBAndChangedCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	metadata, _ := json.Marshal(map[string]string{"github_url": "https://github.com/test/project/blob/" + strings.Repeat("b", 40) + "/README.md", "github_commit": commit})
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: "Readme", EnableStatus: "enabled", Metadata: metadata}}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{{ID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", KnowledgeTitle: "Readme"}}
	answer := service.appendOctoSources(octoCitationContext(), `answer <kb chunk_id="chunk"/>`, refs)
	require.NotContains(t, answer, "github.com")
	require.Contains(t, answer, "未配置公开来源")
	knowledge.rows["doc"].EnableStatus = "disabled"
	answer = service.appendOctoSources(octoCitationContext(), `answer <kb chunk_id="chunk"/>`, refs)
	require.NotContains(t, answer, "来源：")
	knowledge.rows["doc"].EnableStatus = "enabled"
	knowledge.rows["doc"].KnowledgeBaseID = "secret"
	answer = service.appendOctoSources(octoCitationContext(), `answer <kb chunk_id="chunk"/>`, refs)
	require.NotContains(t, answer, "来源：")
}

type octoCitationSourceTool struct {
	types.Tool
	output string
	data   map[string]interface{}
}

func (*octoCitationSourceTool) Name() string { return "source_browse" }
func (t *octoCitationSourceTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Output: t.output, Data: t.data}, nil
}
func TestOctoRawSourceCitationRequiresObservedReadURL(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	url := "https://github.com/test/code/blob/" + strings.Repeat("c", 40) + "/main.go#L3-L7"
	result, _ := json.Marshal(types.SourceRead{Path: "main.go", SourceURL: url, Revision: strings.Repeat("c", 40)})
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(result), data: map[string]interface{}{types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", URL: url, Path: "main.go", Revision: strings.Repeat("c", 40)}}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	service := &Service{}
	answer := service.appendOctoSources(ctx, `answer <web title="main.go" url="`+url+`"/>`, nil)
	require.Contains(t, stripIMCitationTags(answer), url)
	forged := service.appendOctoSources(ctx, `answer <web title="other" url="https://github.com/other/code/blob/main/a.go"/>`, nil)
	require.NotContains(t, stripIMCitationTags(forged), "github.com")
}
func TestOctoCitationURLsDoNotExposeLocalPathsOrTokens(t *testing.T) {
	for _, value := range []string{"file:///etc/passwd", "http://public.example", "https://127.0.0.1/private", "https://host.internal/doc", "https://user:pass@example.org/doc", "https://example.org/doc?token=secret", "javascript:alert(1)"} {
		require.Empty(t, safeOctoSourceURL(value))
	}
	require.Equal(t, "https://github.com/org/repo/blob/main/a.go#L1", safeOctoSourceURL("https://github.com/org/repo/blob/main/a.go#L1"))
}

func TestOctoActualSourceHeadingAndQuotedTitlesUseOnlyExactRetrievedWork(t *testing.T) {
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"one":    {ID: "one", TenantID: 1, KnowledgeBaseID: "kb", Title: "Octo CLI README", EnableStatus: "enabled", Source: "https://example.org/readme"},
		"unused": {ID: "unused", TenantID: 1, KnowledgeBaseID: "kb", Title: "Other retrieved guide", EnableStatus: "enabled", Source: "https://example.org/unused"},
	}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{
		{ID: "chunk-one", KnowledgeID: "one", KnowledgeBaseID: "kb", KnowledgeTitle: "Octo CLI README"},
		{ID: "chunk-unused", KnowledgeID: "unused", KnowledgeBaseID: "kb", KnowledgeTitle: "Other retrieved guide"},
	}
	for _, answer := range []string{"命令。\n**实际来源:**\nOcto CLI README", "命令依据《Octo CLI README》的说明。"} {
		result := service.appendOctoSources(octoCitationContext(), answer, refs)
		require.Contains(t, result, "https://example.org/readme")
		require.NotContains(t, result, "https://example.org/unused")
	}
	for _, answer := range []string{"依据《Octo CLI》的说明。", "依据《Unknown README》的说明。"} {
		result := service.appendOctoSources(octoCitationContext(), answer, refs)
		require.NotContains(t, result, "https://example.org/")
	}
	ambiguous := append(refs, &types.SearchResult{ID: "second-chunk", KnowledgeID: "second-doc", KnowledgeBaseID: "kb", KnowledgeTitle: "Octo CLI README"})
	result := service.appendOctoSources(octoCitationContext(), "根据《Octo CLI README》。", ambiguous)
	require.NotContains(t, result, "https://example.org/", "identical titles from different retrieved documents remain ambiguous")
}

func TestOctoSourceAcceptsNativeManualMetadataWithNumericVersion(t *testing.T) {
	commit := strings.Repeat("a", 40)
	url := "https://github.com/test/project/blob/" + commit + "/README.md"
	metadata, _ := json.Marshal(map[string]any{"format": "markdown", "status": "publish", "version": 3, "content": "Original content", "github_url": url, "github_commit": commit})
	require.Equal(t, url, persistedOctoSource(&types.Knowledge{Source: "manual", Metadata: metadata}))
}
