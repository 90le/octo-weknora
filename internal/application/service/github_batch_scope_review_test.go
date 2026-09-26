package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	githubconnector "github.com/Tencent/WeKnora/internal/datasource/connector/github"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func reviewedBatchContext() context.Context {
	return types.WithPrincipal(context.Background(), types.Principal{Type: types.PrincipalWebUser, ID: "owner-one"})
}

func reviewedBatchFixture(t *testing.T, truncated bool, statuses ...int) (*DataSourceService, func()) {
	t.Helper()
	sha := strings.Repeat("c", 40)
	return reviewedBatchFixtureWithFiles(t, truncated, []map[string]any{
		{"path": "README.md", "type": "blob", "mode": "100644", "sha": sha, "size": 100},
		{"path": "src/app.go", "type": "blob", "mode": "100644", "sha": sha, "size": 200},
		{"path": "images/logo.webp", "type": "blob", "mode": "100644", "sha": sha, "size": 300},
		{"path": "secrets/passwords.md", "type": "blob", "mode": "100644", "sha": sha, "size": 400},
	}, statuses...)
}

func reviewedBatchFixtureWithFiles(t *testing.T, truncated bool, files []map[string]any, statuses ...int) (*DataSourceService, func()) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	treeSHA := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if len(statuses) > 0 && r.URL.Path == "/repos/example/repo" {
			if statuses[0] == http.StatusForbidden {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().UTC().Add(time.Hour).Unix()))
			} else if statuses[0] == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "60")
			}
			w.WriteHeader(statuses[0])
			return
		}
		switch r.URL.Path {
		case "/repos/example/repo":
			_ = json.NewEncoder(w).Encode(map[string]string{"full_name": "example/repo", "default_branch": "main"})
		case "/repos/example/repo/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": commit, "commit": map[string]any{"tree": map[string]string{"sha": treeSHA}}})
		case "/repos/example/repo/git/trees/" + treeSHA:
			require.Equal(t, "1", r.URL.Query().Get("recursive"))
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": truncated, "tree": files})
		default:
			http.NotFound(w, r)
		}
	}))
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(githubconnector.NewConnectorWithHTTPClient(server.Client(), server.URL)))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "reviewed-batch.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}))
	return &DataSourceService{connectorRegistry: registry, dsRepo: repository.NewDataSourceRepository(db),
		kbService: migrationKBService{knowledgeBases: map[string]*types.KnowledgeBase{
			"kb-one": {ID: "kb-one", TenantID: 7},
		}}}, server.Close
}

func TestGitHubBatchPreviewRateLimitCannotIssueCreationTicket(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			service, stop := reviewedBatchFixture(t, false, status)
			defer stop()
			request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one",
				Owner: "example", Repository: "example/repo", Ref: "main", Mode: "documents"}
			preview, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
			require.NoError(t, err)
			require.Equal(t, "error", preview.TreeState)
			require.NotEmpty(t, preview.ErrorCode)
			require.Empty(t, preview.PreviewToken)
		})
	}
}

func TestGitHubBatchRequestOnlyPreviewRequiresCompleteMatchingScope(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	paths := []string{"README.md"}
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "documents", Paths: &paths}
	preview, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.Equal(t, "complete", preview.TreeState)
	require.Equal(t, 1, preview.Summary.EligibleFiles)
	require.NotEmpty(t, preview.PreviewToken)
	require.NotEmpty(t, preview.Commit)
	encoded := strings.Split(preview.PreviewToken, ".")[0]
	tokenBody, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.NotContains(t, string(tokenBody), "example/repo")
	require.NotContains(t, string(tokenBody), "README.md")
	require.NotContains(t, string(tokenBody), "owner-one")
	candidate := types.GitHubRepositoryCandidate{Repository: "example/repo", DefaultBranch: "main",
		Paths: &paths, PreviewToken: preview.PreviewToken}
	batch := &types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example", Mode: "documents",
		ScopeReviewRequired: true}
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: githubBatchEffectiveSettings(
		candidate.Repository, candidate.DefaultBranch, batch.Mode, batch.Paths, batch.Exclude, candidate, true)}
	require.NoError(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	batch.Mode = "source"
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	batch.Mode = "documents"
	candidate.DefaultBranch = "feature"
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	candidate.DefaultBranch = "main"
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	changedExclude := []string{"docs"}
	candidate.Exclude = &changedExclude
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	candidate.Exclude = nil
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	candidate.Repository = "example/another"
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	candidate.Repository = "example/repo"
	changed := []string{"src"}
	candidate.Paths = &changed
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	candidate.Paths = &paths
	config.Settings = githubBatchEffectiveSettings(candidate.Repository, candidate.DefaultBranch, batch.Mode,
		batch.Paths, batch.Exclude, candidate, true)
	otherPrincipal := types.WithPrincipal(context.Background(), types.Principal{Type: types.PrincipalWebUser, ID: "other"})
	require.Error(t, verifyGitHubBatchScopeReview(otherPrincipal, batch, candidate, config))
	config.Credentials = map[string]interface{}{"access_token": "different-token"}
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config), "preview is bound to the credential identity")
	config.Credentials = nil
	batch.KnowledgeBaseID = "other-kb"
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	request.Credentials = map[string]interface{}{"access_token": "private-gh-token-not-for-ticket"}
	privatePreview, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.NotEmpty(t, privatePreview.PreviewToken)
	privateBody, err := base64.RawURLEncoding.DecodeString(strings.Split(privatePreview.PreviewToken, ".")[0])
	require.NoError(t, err)
	require.NotContains(t, string(privateBody), "private-gh-token-not-for-ticket")
}

func TestGitHubBatchPreviewRejectsUnknownButAllowsExplicitEmpty(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "documents"}
	missing := []string{"missing"}
	request.Paths = &missing
	preview, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.Equal(t, "missing_path", preview.TreeState)
	require.Empty(t, preview.PreviewToken)
	request.Paths = nil
	preview, err = service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.Equal(t, "github_scope_exceeds_import_policy", preview.ErrorCode, "unexcluded webp is known parser-unsupported")
	require.Empty(t, preview.PreviewToken)
	empty := []string{"src"}
	request.Paths = &empty
	preview, err = service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.Equal(t, "complete", preview.TreeState)
	require.Zero(t, preview.Summary.EligibleFiles)
	require.NotEmpty(t, preview.PreviewToken)
	candidate := types.GitHubRepositoryCandidate{Repository: "example/repo", DefaultBranch: "main",
		Paths: &empty, PreviewToken: preview.PreviewToken}
	batch := &types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example", Mode: "documents",
		ScopeReviewRequired: true}
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: githubBatchEffectiveSettings(
		candidate.Repository, candidate.DefaultBranch, batch.Mode, batch.Paths, batch.Exclude, candidate, true)}
	require.Error(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
	candidate.AllowEmpty = true
	require.NoError(t, verifyGitHubBatchScopeReview(reviewedBatchContext(), batch, candidate, config))
}

func TestGitHubBatchReviewedDocumentExclusionMatchesPersistedScope(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	ctx := reviewedBatchContext()
	exclude := []string{"images"}
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "documents", Exclude: &exclude}
	preview, err := service.PreviewGitHubBatchScope(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "complete", preview.TreeState)
	require.Equal(t, 1, preview.Summary.EligibleFiles)
	require.NotEmpty(t, preview.PreviewToken)
	batch := &types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example", Mode: "documents",
		SyncPolicy: "manual", ScopeReviewRequired: true,
		Repositories: []types.GitHubRepositoryCandidate{{Repository: "example/repo", DefaultBranch: "main",
			Exclude: &exclude, PreviewToken: preview.PreviewToken}},
	}
	created, err := service.CreateGitHubBatch(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, "created", created.Results[0].Status)
	rows, err := service.dsRepo.FindByKnowledgeBase(ctx, "kb-one")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	config, err := rows[0].ParseConfig()
	require.NoError(t, err)
	storedExcludes, err := previewStringList(config.Settings, "exclude")
	require.NoError(t, err)
	require.Equal(t, exclude, storedExcludes)

	badGlob := []string{"docs/["}
	request.Exclude = &badGlob
	invalid, err := service.PreviewGitHubBatchScope(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "github_exclusion_invalid", invalid.ErrorCode)
	require.Empty(t, invalid.PreviewToken)
}

func TestGitHubBatchReviewedSourceRejectsInvalidExclusionGlob(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	badGlob := []string{"docs/["}
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "source", Exclude: &badGlob}
	_, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.ErrorIs(t, err, datasource.ErrInvalidConfig)
}

func TestGitHubBatchReviewAllowsMoreThanOneDocumentSyncChunk(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	sha := strings.Repeat("c", 40)
	files := make([]map[string]any, 5)
	for index := range files {
		files[index] = map[string]any{"path": fmt.Sprintf("docs/part-%d.md", index), "type": "blob",
			"mode": "100644", "sha": sha, "size": 15 << 20}
	}
	service, closeServer := reviewedBatchFixtureWithFiles(t, false, files)
	defer closeServer()
	ctx := reviewedBatchContext()
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "documents"}
	preview, err := service.PreviewGitHubBatchScope(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "complete", preview.TreeState)
	require.Equal(t, 5, preview.Summary.EligibleFiles)
	require.Greater(t, preview.Summary.EligibleBytes, int64(githubconnector.DocumentBatchLimitBytes))
	require.Empty(t, preview.ErrorCode)
	require.NotEmpty(t, preview.PreviewToken, "more than one future sync chunk is a warning, not a creation blocker")
	batch := &types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example", Mode: "documents",
		SyncPolicy: "manual", ScopeReviewRequired: true,
		Repositories: []types.GitHubRepositoryCandidate{{Repository: "example/repo", DefaultBranch: "main",
			PreviewToken: preview.PreviewToken}},
	}
	created, err := service.CreateGitHubBatch(ctx, batch)
	require.NoError(t, err)
	require.Equal(t, "created", created.Results[0].Status)
}

func TestGitHubBatchSourcePreviewIsEstimatedAndTruncatedTreeCannotAuthorize(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	paths := []string{"src"}
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "source", Paths: &paths}
	preview, err := service.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.True(t, preview.Estimated)
	require.Equal(t, 1, preview.Summary.EligibleFiles)
	require.NotEmpty(t, preview.PreviewToken)
	truncatedService, stop := reviewedBatchFixture(t, true)
	defer stop()
	preview, err = truncatedService.PreviewGitHubBatchScope(reviewedBatchContext(), request)
	require.NoError(t, err)
	require.Equal(t, "truncated", preview.TreeState)
	require.Empty(t, preview.PreviewToken)
}

func TestGitHubBatchScopeTokenExpires(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	config := &types.DataSourceConfig{Type: types.ConnectorTypeGitHub, Settings: githubBatchSettings(
		"example/repo", "main", "documents", []string{"README.md"}, nil)}
	ctx := reviewedBatchContext()
	token, err := signGitHubBatchScopeReview(ctx, 7, "kb-one", "example/repo", "documents", config,
		strings.Repeat("a", 40), 1, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	paths := []string{"README.md"}
	candidate := types.GitHubRepositoryCandidate{Repository: "example/repo", DefaultBranch: "main", Paths: &paths, PreviewToken: token}
	req := &types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Mode: "documents"}
	require.Error(t, verifyGitHubBatchScopeReview(ctx, req, candidate, config))
}

func TestReviewedGitHubBatchCreationNeverCreatesUnknownScopeOrStartsSync(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!")
	service, closeServer := reviewedBatchFixture(t, false)
	defer closeServer()
	ctx := reviewedBatchContext()
	paths := []string{"README.md"}
	request := &types.GitHubBatchScopePreviewRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example",
		Repository: "example/repo", Ref: "main", Mode: "documents", Paths: &paths}
	base := types.GitHubBatchRequest{TenantID: 7, KnowledgeBaseID: "kb-one", Owner: "example", Mode: "documents",
		SyncPolicy: "manual", ScopeReviewRequired: true,
		Repositories: []types.GitHubRepositoryCandidate{{Repository: "example/repo", DefaultBranch: "main", Paths: &paths}},
	}
	unknown, err := service.CreateGitHubBatch(ctx, &base)
	require.NoError(t, err)
	require.Equal(t, "failed", unknown.Results[0].Status)
	rows, err := service.dsRepo.FindByKnowledgeBase(ctx, "kb-one")
	require.NoError(t, err)
	require.Empty(t, rows)
	preview, err := service.PreviewGitHubBatchScope(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, preview.PreviewToken)
	base.Repositories[0].PreviewToken = preview.PreviewToken
	base.StartSync = true
	_, err = service.CreateGitHubBatch(ctx, &base)
	require.ErrorContains(t, err, "does not start a sync")
	base.StartSync = false
	created, err := service.CreateGitHubBatch(ctx, &base)
	require.NoError(t, err)
	require.Equal(t, "created", created.Results[0].Status)
	rows, err = service.dsRepo.FindByKnowledgeBase(ctx, "kb-one")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	config, err := rows[0].ParseConfig()
	require.NoError(t, err)
	storedPaths, err := previewStringList(config.Settings, "paths")
	require.NoError(t, err)
	require.Equal(t, []string{"README.md"}, storedPaths)
	require.Empty(t, rows[0].SyncSchedule, "reviewed manual creation must not schedule an initial run")
}
