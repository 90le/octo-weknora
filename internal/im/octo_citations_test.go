package im

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/modelcontext"
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

func TestOctoAgentCitationSurvivesOutboundCleanup(t *testing.T) {
	commit := strings.Repeat("a", 40)
	link := "https://github.com/Mininglamp-OSS/octo-cli/blob/" + commit + "/README.md"
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: "Octo CLI README", Source: link, EnableStatus: "enabled"},
	}}
	registry := modelcontext.NewRegistry(true)
	registry.RegisterChunk(modelcontext.ChunkReference{ChunkID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", DocumentTitle: "Octo CLI README"})
	answer := registry.DecodeOutputText(`Octo CLI 可安装。<ref id="c1"/>`)
	service := &Service{knowledgeService: knowledge}
	prepared := service.appendOctoSources(octoCitationContext(), answer, registry.CitedKnowledgeReferences(answer))
	visible := formatIMOutboundAnswer(context.Background(), prepared, nil, nil)
	require.NotContains(t, visible, "<kb")
	require.NotContains(t, visible, "chunk")
	require.Contains(t, visible, "Octo CLI README")
	require.Contains(t, visible, link)
}

func TestOctoStreamingFinalKeepsTrustedKBAndSourceLinksBesideTheirClaims(t *testing.T) {
	commit := strings.Repeat("a", 40)
	kbURL := "https://github.com/example/docs/blob/" + commit + "/README.md"
	codeURL := "https://github.com/example/code/blob/" + commit + "/src/socket.go#L8-L9"
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: "产品说明", Source: kbURL, EnableStatus: "enabled"},
	}}
	refs := []*types.SearchResult{{ID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", KnowledgeTitle: "产品说明"}}
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	read, err := json.Marshal(types.SourceRead{Path: "src/socket.go", SourceURL: codeURL, Revision: commit})
	require.NoError(t, err)
	_, err = octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(read), data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "example/code", URL: codeURL, Path: "src/socket.go", Revision: commit, Citable: true},
	}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	service := &Service{knowledgeService: knowledge}
	parts := IMStreamParts{Mode: IMStreamModeAgent, Answer: `文档说明可以安装。<kb doc="产品说明" chunk_id="chunk" kb_id="kb"/> 源码在这里建连接。<web title="伪造标题" url="` + codeURL + `"/> 请分别核对。`}
	final := service.formatIMStreamFinal(ctx, parts, refs, nil)
	require.NotContains(t, final, "<kb")
	require.NotContains(t, final, "<web")
	require.NotContains(t, final, "chunk")
	require.NotContains(t, final, "伪造标题", "never take citation labels from the model tag")
	require.Less(t, strings.Index(final, "文档说明可以安装。"), strings.Index(final, "[来源：产品说明]"))
	require.Less(t, strings.Index(final, "[来源：产品说明]"), strings.Index(final, "源码在这里建连接。"))
	require.Less(t, strings.Index(final, "源码在这里建连接。"), strings.Index(final, "[来源：src/socket.go]"))
	require.Less(t, strings.Index(final, "[来源：src/socket.go]"), strings.Index(final, "请分别核对。"))
	require.Equal(t, 1, strings.Count(final, kbURL), "inline source must not be repeated in a tail list")
	require.Equal(t, 1, strings.Count(final, codeURL), "inline source must not be repeated in a tail list")
	require.NotContains(t, final, "\n来源：\n")
}

func TestOctoNonStreamInlineSourceRejectsForgedDraftAndCrossKBTags(t *testing.T) {
	commit := strings.Repeat("b", 40)
	validURL := "https://github.com/example/docs/blob/" + commit + "/guide.md"
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"valid": {ID: "valid", TenantID: 1, KnowledgeBaseID: "kb", Title: "有效指南", Source: validURL, EnableStatus: "enabled"},
		"draft": {ID: "draft", TenantID: 1, KnowledgeBaseID: "kb", Title: "未发布", Source: "https://example.org/draft", EnableStatus: "disabled"},
		"other": {ID: "other", TenantID: 1, KnowledgeBaseID: "other-kb", Title: "异库资料", Source: "https://example.org/other", EnableStatus: "enabled"},
	}}
	refs := []*types.SearchResult{
		{ID: "valid-chunk", KnowledgeID: "valid", KnowledgeBaseID: "kb", KnowledgeTitle: "有效指南"},
		{ID: "draft-chunk", KnowledgeID: "draft", KnowledgeBaseID: "kb", KnowledgeTitle: "未发布"},
		{ID: "other-chunk", KnowledgeID: "other", KnowledgeBaseID: "other-kb", KnowledgeTitle: "异库资料"},
	}
	service := &Service{knowledgeService: knowledge}
	answer := `结论一。<kb chunk_id="valid-chunk"/> 结论二。<kb chunk_id="draft-chunk" kb_id="kb"/> 结论三。<kb chunk_id="other-chunk" kb_id="other-kb"/> <web url="https://example.org/private?token=secret"/>`
	visible := stripIMCitationTags(service.appendOctoSources(octoCitationContext(), answer, refs))
	require.Contains(t, visible, "结论一。 [来源：有效指南](<"+validURL+">)")
	require.Equal(t, 1, strings.Count(visible, validURL))
	require.NotContains(t, visible, "未发布")
	require.NotContains(t, visible, "异库资料")
	require.NotContains(t, visible, "other-kb")
	require.NotContains(t, visible, "token=secret")
	require.NotContains(t, visible, "<kb")
	require.NotContains(t, visible, "<web")
}

func TestOctoCitationSettingOffRemovesInlineTagsAndDoesNotAppendSources(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	commit := strings.Repeat("c", 40)
	url := "https://github.com/example/docs/blob/" + commit + "/README.md"
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: "文档", Source: url, EnableStatus: "enabled"},
	}}
	refs := []*types.SearchResult{{ID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", KnowledgeTitle: "文档"}}
	service := &Service{knowledgeService: knowledge}
	answer := `答案。<kb chunk_id="chunk" kb_id="kb"/>`
	octobusiness.SetCitationRenderingEnabled(ctx, false)
	direct := service.appendOctoSources(ctx, answer, refs)
	require.Equal(t, "答案。", direct)
	require.Empty(t, knowledge.requested, "disabled citations must not fetch knowledge")
	final := service.formatIMStreamFinal(ctx, IMStreamParts{Mode: IMStreamModeAgent, Answer: answer}, refs, nil)
	require.Equal(t, "答案。", strings.TrimSpace(final))
	require.NotContains(t, final, url)
}

func TestOctoInlinePrivateTitleNeverRevealsLocalPathOrToken(t *testing.T) {
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: `C:\private\token=secret\ops.md`, Source: "file:///private/ops.md", EnableStatus: "enabled"},
	}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{{ID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", KnowledgeTitle: `C:\private\token=secret\ops.md`}}
	answer := service.appendOctoSources(octoCitationContext(), `答案。<kb chunk_id="chunk" kb_id="kb"/>`, refs)
	require.Contains(t, answer, "（来源：知识库资料）")
	require.NotContains(t, answer, `C:\private`)
	require.NotContains(t, answer, "token=secret")
	require.NotContains(t, answer, "file://")
	for _, unsafe := range []string{
		"Bearer example-secret", "sk-example-secret", "bf_example-secret",
		"app_example-secret", "uk_example-secret", "C:/Users/Administrator/private/ops.md",
		"~/private/ops.md", "./private/ops.md", "../private/ops.md",
		"说明 /home/mlclaw/private/ops.md", "说明 C:/Users/Administrator/private/ops.md",
	} {
		require.Equal(t, "知识库资料", safeOctoSourceTitle(unsafe))
	}
	require.Equal(t, "源码 src/foo.ts", safeOctoSourceTitle("源码 src/foo.ts"))
}

func TestOctoCitedPrivateDocumentKeepsSourceTitleWithoutInventedLink(t *testing.T) {
	knowledge := &octoCitationKnowledge{rows: map[string]*types.Knowledge{
		"doc": {ID: "doc", TenantID: 1, KnowledgeBaseID: "kb", Title: "内部运维指南", Source: "file:///private/ops.md", EnableStatus: "enabled"},
	}}
	service := &Service{knowledgeService: knowledge}
	refs := []*types.SearchResult{{ID: "chunk", KnowledgeID: "doc", KnowledgeBaseID: "kb", KnowledgeTitle: "内部运维指南"}}
	answer := service.appendOctoSources(octoCitationContext(), `根据内部运维指南，操作如下。<kb doc="内部运维指南" chunk_id="chunk" kb_id="kb"/>`, refs)
	visible := stripIMCitationTags(answer)
	require.Contains(t, visible, "来源：\n- 内部运维指南（知识库资料，未配置公开来源链接）")
	require.NotContains(t, visible, "file://")
	require.NotContains(t, visible, "/private/")
	// A public link is never fabricated for a private local document.
	require.NotContains(t, visible, "https://")
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

type octoCitationReleaseTool struct {
	types.Tool
	data map[string]interface{}
}

func (*octoCitationReleaseTool) Name() string { return "github_release_lookup" }
func (t *octoCitationReleaseTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Data: t.data}, nil
}

func (*octoCitationSourceTool) Name() string { return "source_browse" }
func (t *octoCitationSourceTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Output: t.output, Data: t.data}, nil
}
func TestOctoRawSourceCitationRequiresObservedReadURL(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	url := "https://github.com/test/code/blob/" + strings.Repeat("c", 40) + "/main.go#L3-L7"
	result, _ := json.Marshal(types.SourceRead{Path: "main.go", SourceURL: url, Revision: strings.Repeat("c", 40)})
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(result), data: map[string]interface{}{types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: url, Path: "main.go", Revision: strings.Repeat("c", 40), Citable: true}}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	service := &Service{}
	answer := service.appendOctoSources(ctx, `answer <web title="main.go" url="`+url+`"/>`, nil)
	require.Contains(t, stripIMCitationTags(answer), url)
	forged := service.appendOctoSources(ctx, `answer <web title="other" url="https://github.com/other/code/blob/main/a.go"/>`, nil)
	require.NotContains(t, stripIMCitationTags(forged), "github.com")
}

func TestOctoCodeLinksRequireNarrowReadAndTrustedRepository(t *testing.T) {
	revision := strings.Repeat("a", 40)
	base := "https://github.com/test/code/blob/" + revision + "/src/main.go"
	wideURL := base + "#L1-L35"
	narrowURL := base + "#L4-L5"
	shortURL := base + "#L4-L4"
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	track := func(marker types.SourceBrowseCitation) {
		t.Helper()
		_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{data: map[string]interface{}{types.SourceBrowseCitationDataKey: marker}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
		require.NoError(t, err)
	}
	track(types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: wideURL, Path: "src/main.go", Revision: revision, Citable: false})
	service := &Service{}
	wide := service.appendOctoSources(ctx, `宽读说明。<web url="`+wideURL+`"/> [看第4行](`+shortURL+`)`, nil)
	require.NotContains(t, wide, wideURL, "a 35-line exploratory read cannot become a clickable web source")
	require.NotContains(t, wide, shortURL, "nor can a handwritten subset borrow its provenance")
	require.Contains(t, wide, "看第4行（源码行号未核验）")
	require.NotContains(t, wide, "\n来源：", "rejected source cannot reappear in the tail list")

	track(types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: narrowURL, Path: "src/main.go", Revision: revision, Citable: true})
	narrow := service.appendOctoSources(ctx, `窄读说明。<web url="`+narrowURL+`"/> [看第4行](`+shortURL+`)`, nil)
	require.Contains(t, narrow, narrowURL)
	require.Contains(t, narrow, "[看第4行]("+shortURL+")")
	aliasURL := strings.Replace(shortURL, "https://github.com/", "https://www.github.com/", 1)
	alias := service.appendOctoSources(ctx, `别名。<web url="`+aliasURL+`"/> [别名行](`+aliasURL+`)`, nil)
	require.NotContains(t, alias, aliasURL, "alternate GitHub hosts cannot bypass canonical repository checks")
	require.Contains(t, alias, "别名行（源码行号未核验）")
	dottedURL := strings.Replace(shortURL, "github.com/", "github.com./", 1)
	dotted := service.appendOctoSources(ctx, `[尾点域名](`+dottedURL+`)`, nil)
	require.NotContains(t, dotted, dottedURL)

	forgedCtx := octobusiness.WithRetrievalTrace(octoCitationContext())
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "other/repo", URL: narrowURL, Path: "src/main.go", Revision: revision, Citable: true},
	}}).Execute(forgedCtx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	forged := service.appendOctoSources(forgedCtx, `错误归属。<web url="`+narrowURL+`"/> [看第4行](`+shortURL+`)`, nil)
	require.NotContains(t, forged, narrowURL, "URL owner/repo must match the trusted marker")
	require.NotContains(t, forged, shortURL)
	require.NotContains(t, forged, "\n来源：")
}

func TestOctoGitHubLineLinksRequireCurrentTurnReadRange(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	revision := strings.Repeat("c", 40)
	readURL := "https://github.com/test/code/blob/" + revision + "/main.go#L3-L7"
	result, _ := json.Marshal(types.SourceRead{Path: "main.go", SourceURL: readURL, Revision: revision})
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(result), data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: readURL, Path: "main.go", Revision: revision, Citable: true},
	}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)

	wrongRange := "https://github.com/test/code/blob/" + revision + "/main.go#L10-L20"
	shortRange := "https://github.com/test/code/blob/" + revision + "/main.go#L4-L4"
	wrongRepo := "https://github.com/other/code/blob/" + revision + "/main.go#L3-L7"
	branchLink := "https://github.com/test/code/blob/main/main.go#L3-L7"
	encodedRange := "https://github.com/test/code/blob/" + revision + "/main.go#%4C10-%4C20"
	release := "https://github.com/test/code/releases/tag/v1.0"
	answer := "[已读](" + readURL + ")、[短行](" + shortRange + ")、[错行](" + wrongRange + ")、[错库](" + wrongRepo + ")、[分支行](" + branchLink + ")、[版本](" + release + ")；裸链 " + wrongRange + "；编码 " + encodedRange + "；HTML <a href=\"" + wrongRepo + "\">代码</a>"
	got := sanitizeOctoGitHubLineLinks(ctx, answer)
	require.Contains(t, got, "[已读]("+readURL+")")
	require.Contains(t, got, "[短行]("+shortRange+")", "a short line within an authorized read remains clickable")
	require.Contains(t, got, "错行（源码行号未核验）")
	require.Contains(t, got, "错库（源码行号未核验）")
	require.Contains(t, got, "分支行（源码行号未核验）")
	require.Contains(t, got, "[版本]("+release+")")
	require.NotContains(t, got, wrongRange)
	require.NotContains(t, got, wrongRepo)
	require.NotContains(t, got, branchLink)
	require.NotContains(t, got, encodedRange)
	require.Contains(t, got, "未经核验的源码行号链接已省略")
	require.Equal(t, answer, sanitizeOctoGitHubLineLinks(context.Background(), answer), "non-Octo answers are untouched")
}

func TestOctoLineSubsetMustBeShortAndWithinOnePinnedRead(t *testing.T) {
	revision := strings.Repeat("d", 40)
	base := "https://github.com/test/code/blob/" + revision + "/src/socket.ts"
	observed := map[string]bool{base + "#L1-L200": true}
	require.True(t, octoObservedGitHubLineURL(base+"#L2", observed))
	require.True(t, octoObservedGitHubLineURL(base+"#L1700", map[string]bool{base + "#L1688-L1778": true}))
	require.False(t, octoObservedGitHubLineURL(base+"#L1700-L1769", map[string]bool{base + "#L1688-L1778": true}), "the earlier wrong broad range stays untrusted")
	require.True(t, octoObservedGitHubLineURL(base+"#%4C38", observed), "encoded line markers are parsed but not exempted")
	require.False(t, octoObservedGitHubLineURL(base+"#L10-L90", observed), "a broad manually written range is not a precise citation")
	require.False(t, octoObservedGitHubLineURL(base+"#L201", observed))
	require.False(t, octoObservedGitHubLineURL("https://github.com/other/code/blob/"+revision+"/src/socket.ts#L2", observed))
	require.False(t, octoObservedGitHubLineURL("https://github.com/test/code/blob/main/src/socket.ts#L2", observed))
	require.False(t, octoObservedGitHubLineURL(base+"#L0", observed))
}

func TestOctoStreamingFinalRendersOnlyObservedCodeLineSource(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	revision := strings.Repeat("a", 40)
	readURL := "https://github.com/test/code/blob/" + revision + "/main.go#L3-L7"
	wrongURL := "https://github.com/test/code/blob/" + revision + "/main.go#L30-L70"
	result, _ := json.Marshal(types.SourceRead{Path: "main.go", SourceURL: readURL, Revision: revision})
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(result), data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: readURL, Path: "main.go", Revision: revision, Citable: true},
	}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	service := &Service{}
	parts := IMStreamParts{Mode: IMStreamModeAgent, Answer: "结论。<web title=\"main.go\" url=\"" + readURL + "\"/> [错误引用](" + wrongURL + ")"}
	final := service.formatIMStreamFinal(ctx, parts, nil, nil)
	require.Contains(t, final, "结论。")
	require.Contains(t, final, readURL)
	require.Contains(t, final, "错误引用（源码行号未核验）")
	require.NotContains(t, final, wrongURL)
	require.NotContains(t, final, "<web")
}

func TestOctoBranchLineURLIsNotAStableSourceEvenWhenRead(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	branchURL := "https://github.com/test/code/blob/main/main.go#L3-L7"
	result, _ := json.Marshal(types.SourceRead{Path: "main.go", SourceURL: branchURL, Revision: "snapshot:current"})
	_, err := octobusiness.TrackRetrieval(&octoCitationSourceTool{output: string(result), data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", Repository: "test/code", URL: branchURL, Path: "main.go", Revision: "snapshot:current", Citable: true},
	}}).Execute(ctx, []byte(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	answer := (&Service{}).appendOctoSources(ctx, "结论。<web title=\"main.go\" url=\""+branchURL+"\"/> [源码]("+branchURL+")", nil)
	require.NotContains(t, answer, branchURL)
	require.Contains(t, answer, "源码（源码行号未核验）")
	require.Empty(t, persistedOctoSource(&types.Knowledge{Source: branchURL}))
}

func TestOctoOfficialReleaseCitationIsAppendedWithoutModelRefAndRespectsSetting(t *testing.T) {
	ctx := octobusiness.WithRetrievalTrace(octoCitationContext())
	url := "https://github.com/test/octo-web/releases/tag/v1.2.3"
	_, err := octobusiness.TrackRetrieval(&octoCitationReleaseTool{data: map[string]interface{}{
		types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
			KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "test/octo-web", TagName: "v1.2.3", URL: url,
			PublishedAt: time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC), CheckedAt: time.Date(2026, time.September, 22, 1, 0, 0, 0, time.UTC),
		},
	}}).Execute(ctx, []byte(`{"action":"latest","release_ref":"r1"}`))
	require.NoError(t, err)
	service := &Service{}
	answer := service.appendOctoSources(ctx, "Web 客户端已发布 v1.2.3。", nil)
	require.Contains(t, answer, url)
	require.Contains(t, answer, "test/octo-web · v1.2.3")

	octobusiness.SetCitationRenderingEnabled(ctx, false)
	withoutCitations := service.appendOctoSources(ctx, "Web 客户端已发布 v1.2.3。", nil)
	require.NotContains(t, withoutCitations, url)
}
func TestOctoCitationURLsDoNotExposeLocalPathsOrTokens(t *testing.T) {
	for _, value := range []string{
		"file:///etc/passwd", "http://public.example", "https://127.0.0.1/private",
		"https://host.internal/doc", "https://user:pass@example.org/doc",
		"https://example.org/doc?token=secret", "javascript:alert(1)",
		"https://example.org/files/sk-1234567890abcdef1234",
		"https://example.org/files/bf_1234567890abcdef1234",
		"https://example.org/files/app_1234567890abcdef1234",
		"https://example.org/files/uk_1234567890abcdef1234",
		"https://example.org/file/token%3Dsecret",
		"https://example.org/file#token=secret",
		"https://example.org/file#Bearer%20example-secret",
	} {
		require.Empty(t, safeOctoSourceURL(value))
	}
	require.Equal(t, "https://github.com/org/repo/blob/main/a.go#L1", safeOctoSourceURL("https://github.com/org/repo/blob/main/a.go#L1"))
	commit := strings.Repeat("a", 40)
	for _, path := range []string{"src/app_auth.go", "src/uk_channel.go", "src/token.go"} {
		link := "https://github.com/org/repo/blob/" + commit + "/" + path + "#L2-L5"
		require.Equal(t, link, safeOctoSourceURL(link), "ordinary pinned GitHub source paths must remain clickable")
	}
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
