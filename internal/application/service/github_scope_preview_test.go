package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type githubPreviewSourceRepo struct {
	interfaces.DataSourceRepository
	source *types.DataSource
}

func (r *githubPreviewSourceRepo) FindByID(_ context.Context, id string) (*types.DataSource, error) {
	if r.source != nil && r.source.ID == id {
		return r.source, nil
	}
	return nil, datasource.ErrDataSourceNotFound
}

type githubPreviewKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s *githubPreviewKBService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if s.kb != nil && s.kb.ID == id {
		return s.kb, nil
	}
	return nil, datasource.ErrKnowledgeBaseNotFound
}

func githubPreviewServiceFixture(t *testing.T, treeEntries []map[string]any, truncated bool) (*DataSourceService, *atomic.Int32, *atomic.Int32, func()) {
	t.Helper()
	commit, treeSHA, blobSHA := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, entry := range treeEntries {
		entry["sha"] = blobSHA
	}
	var calls, blobs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/example/repo":
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "example/repo", "default_branch": "main"})
		case "/repos/example/repo/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": commit, "commit": map[string]any{"tree": map[string]string{"sha": treeSHA}}})
		case "/repos/example/repo/git/trees/" + treeSHA:
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": treeEntries, "truncated": truncated})
		default:
			if strings.Contains(r.URL.Path, "/git/blobs/") {
				blobs.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	config, err := json.Marshal(types.DataSourceConfig{
		Settings:    map[string]interface{}{"repository": "example/repo", "mode": "source", "paths": nil},
		Credentials: map[string]interface{}{"access_token": "test-private-token"},
	})
	require.NoError(t, err)
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(github.NewConnectorWithHTTPClient(server.Client(), server.URL)))
	svc := &DataSourceService{
		dsRepo: &githubPreviewSourceRepo{source: &types.DataSource{
			ID: "source-a", TenantID: 7, KnowledgeBaseID: "kb-a", Type: types.ConnectorTypeGitHub, Config: config,
		}},
		kbService:         &githubPreviewKBService{kb: &types.KnowledgeBase{ID: "kb-a", TenantID: 7}},
		connectorRegistry: registry,
	}
	return svc, &calls, &blobs, server.Close
}

func previewFile(path string, size int64) map[string]any {
	return map[string]any{"path": path, "type": "blob", "mode": "100644", "size": size}
}

func TestGitHubDocumentScopePreviewUsesCurrentPathsAndShowsProposedOnlyExclude(t *testing.T) {
	entries := []map[string]any{
		previewFile("README.md", 100), previewFile("docs/setup.mdx", 200),
		previewFile("docs/logo.webp", 300), previewFile("images/banner.png", 18<<20),
		previewFile("src/app.js", 400),
	}
	for i := range 5 {
		entries = append(entries, previewFile(fmt.Sprintf("docs/large-%d.md", i), 15<<20))
	}
	svc, calls, blobs, closeServer := githubPreviewServiceFixture(t, entries, false)
	defer closeServer()
	stored := append([]byte(nil), svc.dsRepo.(*githubPreviewSourceRepo).source.Config...)
	paths := []string{"README.md", "docs"}
	exclude := []string{"docs/large-*.md"}
	resp, err := svc.PreviewGitHubDocumentScope(context.Background(), 7, "kb-a", &types.GitHubDocumentScopePreviewRequest{
		SourceID: "source-a", Paths: &paths, Exclude: &exclude,
	})
	require.NoError(t, err)
	require.Equal(t, "complete", resp.TreeState)
	require.Equal(t, strings.Repeat("a", 40), resp.Commit)
	require.Equal(t, []string{"README.md", "docs"}, resp.PreviewPaths)
	require.True(t, resp.PathsOverridden)
	require.False(t, resp.FullRepository)
	require.False(t, resp.ExclusionsAppliedBySync)
	require.Equal(t, 8, resp.ActualSync.CandidateFiles)
	require.Equal(t, 1, resp.ActualSync.ImageFiles)
	require.Equal(t, int64(300), resp.ActualSync.ImageBytes)
	require.Equal(t, 1, resp.ActualSync.ParserUnsupportedFiles, "webp is selected by GitHub but not accepted by the current importer")
	require.Contains(t, resp.ActualSync.Warnings, "current_sync_will_fail_unsupported")
	require.Contains(t, resp.ActualSync.Warnings, "batch_limit_if_all_changed")
	require.NotNil(t, resp.ProposedAfterExclude)
	require.Equal(t, 3, resp.ProposedAfterExclude.CandidateFiles)
	require.Contains(t, resp.Warnings, "exclude_not_applied_by_current_sync")
	require.Equal(t, int32(3), calls.Load(), "preview must only request repo, commit and tree")
	require.Zero(t, blobs.Load())
	require.Equal(t, stored, []byte(svc.dsRepo.(*githubPreviewSourceRepo).source.Config), "preview cannot persist selection")
	encoded, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "test-private-token")
}

func TestGitHubDocumentScopePreviewFullRepoLimitsTruncationAndNoBlob(t *testing.T) {
	entries := make([]map[string]any, 0, 2001)
	for i := range 2001 {
		entries = append(entries, previewFile(fmt.Sprintf("docs/%04d.md", i), 1))
	}
	svc, calls, blobs, closeServer := githubPreviewServiceFixture(t, entries, true)
	defer closeServer()
	resp, err := svc.PreviewGitHubDocumentScope(context.Background(), 7, "kb-a", &types.GitHubDocumentScopePreviewRequest{SourceID: "source-a"})
	require.NoError(t, err)
	require.Equal(t, "truncated", resp.TreeState)
	require.True(t, resp.FullRepository)
	require.False(t, resp.PathsOverridden)
	require.Equal(t, 2001, resp.ActualSync.CandidateFiles)
	require.Len(t, resp.ActualSync.SamplePaths, githubScopePreviewMaxSample)
	require.Contains(t, resp.ActualSync.Warnings, "document_count_limit")
	require.Contains(t, resp.Warnings, "tree_truncated_counts_are_lower_bounds")
	require.Equal(t, int32(3), calls.Load())
	require.Zero(t, blobs.Load())
}

func TestGitHubDocumentScopePreviewDeniesCrossTenantAndOtherKBWithoutGitHubCall(t *testing.T) {
	svc, calls, blobs, closeServer := githubPreviewServiceFixture(t, []map[string]any{previewFile("README.md", 1)}, false)
	defer closeServer()
	req := &types.GitHubDocumentScopePreviewRequest{SourceID: "source-a"}
	_, err := svc.PreviewGitHubDocumentScope(context.Background(), 8, "kb-a", req)
	require.ErrorIs(t, err, datasource.ErrKnowledgeBaseNotFound)
	_, err = svc.PreviewGitHubDocumentScope(context.Background(), 7, "other-kb", req)
	require.ErrorIs(t, err, datasource.ErrKnowledgeBaseNotFound)
	svc.dsRepo.(*githubPreviewSourceRepo).source.KnowledgeBaseID = "other-kb"
	_, err = svc.PreviewGitHubDocumentScope(context.Background(), 7, "kb-a", req)
	require.ErrorIs(t, err, datasource.ErrDataSourceNotFound)
	require.Zero(t, calls.Load())
	require.Zero(t, blobs.Load())
}

func TestGitHubDocumentScopePreviewInvalidAndMissingPath(t *testing.T) {
	svc, calls, blobs, closeServer := githubPreviewServiceFixture(t, []map[string]any{previewFile("README.md", 1)}, false)
	defer closeServer()
	invalid := []string{"../private"}
	resp, err := svc.PreviewGitHubDocumentScope(context.Background(), 7, "kb-a", &types.GitHubDocumentScopePreviewRequest{SourceID: "source-a", Paths: &invalid})
	require.NoError(t, err)
	require.Equal(t, "error", resp.TreeState)
	require.Equal(t, "github_selection_invalid", resp.ErrorCode)
	require.Zero(t, calls.Load(), "invalid path must be rejected before any HTTP request")
	missing := []string{"missing"}
	resp, err = svc.PreviewGitHubDocumentScope(context.Background(), 7, "kb-a", &types.GitHubDocumentScopePreviewRequest{SourceID: "source-a", Paths: &missing})
	require.NoError(t, err)
	require.Equal(t, "missing_path", resp.TreeState)
	require.Equal(t, "github_selected_path_missing", resp.ErrorCode)
	require.NotEmpty(t, resp.Commit, "missing path still has a pinned tree revision")
	require.Zero(t, blobs.Load())
}
