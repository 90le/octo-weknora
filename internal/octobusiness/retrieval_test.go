package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type fakeRetrieval struct {
	types.Tool
	success bool
	err     error
}

type fakeOtherTool struct {
	*fakeRetrieval
	name string
}

type sourceCitationTool struct {
	types.Tool
	result *types.ToolResult
}

type githubReleaseCitationTool struct {
	types.Tool
	result *types.ToolResult
}

func (*githubReleaseCitationTool) Name() string { return "github_release_lookup" }

func (t *githubReleaseCitationTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return t.result, nil
}

func (*sourceCitationTool) Name() string { return "source_browse" }

func (t *sourceCitationTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return t.result, nil
}

func (t *fakeOtherTool) Name() string { return t.name }

func TestOnlyKnowledgeRetrievalChangesEvidence(t *testing.T) {
	ctx := WithRetrievalTrace(context.Background())
	thinking := &fakeOtherTool{fakeRetrieval: &fakeRetrieval{success: true}, name: "thinking"}
	require.Same(t, thinking, TrackRetrieval(thinking), "other tools retain their original optional interfaces")
	_, err := TrackRetrieval(thinking).Execute(ctx, nil)
	require.NoError(t, err)
	require.False(t, retrievalReady(ctx), "thinking is not knowledge search")
	_, err = TrackRetrieval(&fakeRetrieval{success: true}).Execute(ctx, nil)
	require.NoError(t, err)
	require.True(t, retrievalReady(ctx))
	for _, name := range []string{"octo_knowledge_operations", "contacts", "todo_write"} {
		tool := &fakeOtherTool{fakeRetrieval: &fakeRetrieval{err: errors.New("business operation denied")}, name: name}
		require.Same(t, tool, TrackRetrieval(tool))
		_, err = TrackRetrieval(tool).Execute(ctx, nil)
		require.Error(t, err)
		require.True(t, retrievalReady(ctx), "unrelated business errors must not poison successful knowledge retrieval")
	}
}

func (*fakeRetrieval) Name() string { return "knowledge_search" }

func (t *fakeRetrieval) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Success: t.success}, t.err
}
func TestMissingIssueRequiresTrustedSearchAndRejectsLatchedFailure(t *testing.T) {
	service := testService(t)
	ctx := WithRetrievalTrace(principalContext(testPrincipal()))
	_, err := service.CreateIssue(ctx, gapInput())
	require.ErrorIs(t, err, ErrInvalid, "no model claim can replace an actual search")
	_, err = TrackRetrieval(&fakeRetrieval{success: true}).Execute(ctx, nil)
	require.NoError(t, err)
	_, err = service.CreateIssue(ctx, gapInput())
	require.NoError(t, err)
	failCtx := WithRetrievalTrace(principalContext(testPrincipal()))
	_, err = TrackRetrieval(&fakeRetrieval{err: errors.New("retrieval outage")}).Execute(failCtx, nil)
	require.Error(t, err)
	_, err = TrackRetrieval(&fakeRetrieval{success: true}).Execute(failCtx, nil)
	require.NoError(t, err)
	_, err = service.CreateIssue(failCtx, gapInput())
	require.ErrorIs(t, err, ErrInvalid, "a partial retrieval failure prevents unsupported gap registration")
	clean := WithRetrievalTrace(principalContext(testPrincipal()))
	require.False(t, retrievalReady(clean), "previous turn evidence must not bleed into another turn")
}

func TestMissingIssueHonorsAnswerEvidenceContract(t *testing.T) {
	service := testService(t)

	// principalContext contains a successful retrieval trace, the same state
	// that formerly let a search-only source answer create a false knowledge
	// gap. A source contract still requires an actual source read.
	sourcePrincipal := testPrincipal()
	sourcePrincipal.MessageID = "source-message"
	sourceCtx := answerevidence.WithContract(principalContext(sourcePrincipal), "这个函数的源码如何实现？")
	_, err := service.CreateIssue(sourceCtx, gapInput())
	require.ErrorIs(t, err, ErrInvalid)
	answerevidence.RecordSourceRead(sourceCtx)
	_, err = service.CreateIssue(sourceCtx, gapInput())
	require.NoError(t, err)

	// A release/integration prompt has the same protection: a generic search
	// success cannot make absence of RAG material into a “missing” issue.
	releasePrincipal := testPrincipal()
	releasePrincipal.MessageID = "release-message"
	releaseCtx := answerevidence.WithContract(principalContext(releasePrincipal), "Claude 支持接入 Octo IM 吗？")
	_, err = service.CreateIssue(releaseCtx, gapInput())
	require.ErrorIs(t, err, ErrInvalid)
	answerevidence.RecordDocumentEvidence(releaseCtx)
	_, err = service.CreateIssue(releaseCtx, gapInput())
	require.NoError(t, err)
}

func TestSourceBrowseReadTracksPrivateProvenanceWithSourceRef(t *testing.T) {
	ctx := WithRetrievalTrace(context.Background())
	url := "https://github.com/example/repo/blob/0123456789012345678901234567890123456789/main.go#L3-L7"
	tool := &sourceCitationTool{result: &types.ToolResult{Success: true, Data: map[string]interface{}{
		types.SourceBrowseCitationDataKey: types.SourceBrowseCitation{KnowledgeBaseID: "kb", URL: url, Path: "main.go", Revision: "0123456789012345678901234567890123456789"},
	}}}
	_, err := TrackRetrieval(tool).Execute(ctx, json.RawMessage(`{"action":"read","source_ref":"s1"}`))
	require.NoError(t, err)
	require.Equal(t, []SourceCitation{{KnowledgeBaseID: "kb", URL: url, Path: "main.go", Revision: "0123456789012345678901234567890123456789"}}, SourceCitations(ctx))
}

func TestGitHubReleaseLookupTracksTrustedOfficialCitationWithoutChangingRetrievalReady(t *testing.T) {
	ctx := WithRetrievalTrace(context.Background())
	url := "https://github.com/example/octo-web/releases/tag/v1.2.3"
	tool := &githubReleaseCitationTool{result: &types.ToolResult{Success: true, Data: map[string]interface{}{
		types.GitHubReleaseCitationDataKey: types.GitHubReleaseCitation{
			KnowledgeBaseID: "kb", DataSourceID: "source", Repository: "example/octo-web", TagName: "v1.2.3", URL: url,
			PublishedAt: time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC), CheckedAt: time.Date(2026, time.September, 22, 1, 0, 0, 0, time.UTC),
		},
	}}}
	_, err := TrackRetrieval(tool).Execute(ctx, json.RawMessage(`{"action":"latest","release_ref":"r1"}`))
	require.NoError(t, err)
	require.False(t, retrievalReady(ctx), "release metadata is a citation source, not a generic KB retrieval for missing-issue gating")
	require.Equal(t, []SourceCitation{{KnowledgeBaseID: "kb", URL: url, Title: "example/octo-web · v1.2.3", OfficialRelease: true}}, SourceCitations(ctx))

	SetCitationRenderingEnabled(ctx, false)
	require.False(t, CitationRenderingEnabled(ctx))
}
