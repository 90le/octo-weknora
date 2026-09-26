package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type githubScopePreviewHandlerService struct {
	interfaces.DataSourceService
	calls    int
	response *types.GitHubDocumentScopePreview
}

func (s *githubScopePreviewHandlerService) PreviewGitHubDocumentScope(_ context.Context, tenantID uint64, kbID string, req *types.GitHubDocumentScopePreviewRequest) (*types.GitHubDocumentScopePreview, error) {
	s.calls++
	if tenantID != 7 || kbID != "kb-a" || req.SourceID != "source-a" {
		panic("handler did not bind preview to the expected KB/source")
	}
	return s.response, nil
}

type githubScopePreviewHandlerKB struct {
	interfaces.KnowledgeBaseService
	tenantID uint64
}

func (s *githubScopePreviewHandlerKB) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: id, TenantID: s.tenantID}, nil
}

func githubScopePreviewHandlerRouter(service *githubScopePreviewHandlerService, kb *githubScopePreviewHandlerKB, tenantID uint64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewDataSourceHandler(service, kb)
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), tenantID)
		c.Next()
	})
	r.POST("/knowledge-bases/:id/github-document-scope-preview", h.PreviewGitHubDocumentScope)
	return r
}

func TestGitHubScopePreviewHandlerRejectsCrossTenantBeforeSourceLookup(t *testing.T) {
	service := &githubScopePreviewHandlerService{}
	r := githubScopePreviewHandlerRouter(service, &githubScopePreviewHandlerKB{tenantID: 8}, 7)
	req := httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb-a/github-document-scope-preview", strings.NewReader(`{"source_id":"source-a"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, service.calls)
}

func TestGitHubScopePreviewHandlerReturnsBoundedStructuredResult(t *testing.T) {
	service := &githubScopePreviewHandlerService{response: &types.GitHubDocumentScopePreview{
		SourceID: "source-a", Repository: "example/repo", Commit: strings.Repeat("a", 40), TreeState: "complete",
		ActualSync: types.GitHubDocumentPreviewSummary{CandidateFiles: 2},
	}}
	r := githubScopePreviewHandlerRouter(service, &githubScopePreviewHandlerKB{tenantID: 7}, 7)
	req := httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb-a/github-document-scope-preview", strings.NewReader(`{"source_id":"source-a"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, service.calls)
	var parsed types.GitHubDocumentScopePreview
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
	require.Equal(t, 2, parsed.ActualSync.CandidateFiles)
	require.NotContains(t, w.Body.String(), "credentials")

	service.response.TreeState, service.response.ErrorCode = "missing_path", "github_selected_path_missing"
	req = httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb-a/github-document-scope-preview", strings.NewReader(`{"source_id":"source-a"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	service.response.TreeState, service.response.ErrorCode = "error", "github_exclusion_invalid"
	req = httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb-a/github-document-scope-preview", strings.NewReader(`{"source_id":"source-a"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGitHubScopePreviewHandlerRejectsOversizedRequestBeforeService(t *testing.T) {
	service := &githubScopePreviewHandlerService{}
	r := githubScopePreviewHandlerRouter(service, &githubScopePreviewHandlerKB{tenantID: 7}, 7)
	body := append([]byte(`{"source_id":"source-a","paths":["`), bytes.Repeat([]byte{'x'}, 70<<10)...)
	body = append(body, []byte(`"]}`)...)
	req := httptest.NewRequest(http.MethodPost, "/knowledge-bases/kb-a/github-document-scope-preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, service.calls)
}
