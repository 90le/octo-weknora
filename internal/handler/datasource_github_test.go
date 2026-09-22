package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type githubDataSourceServiceStub struct {
	interfaces.DataSourceService
	discover func(context.Context, *types.GitHubDiscoveryRequest) (*types.GitHubDiscoveryResponse, error)
	batch    func(context.Context, *types.GitHubBatchRequest) (*types.GitHubBatchResponse, error)
}

func (s *githubDataSourceServiceStub) DiscoverGitHubRepositories(ctx context.Context, req *types.GitHubDiscoveryRequest) (*types.GitHubDiscoveryResponse, error) {
	return s.discover(ctx, req)
}

func (s *githubDataSourceServiceStub) CreateGitHubBatch(ctx context.Context, req *types.GitHubBatchRequest) (*types.GitHubBatchResponse, error) {
	return s.batch(ctx, req)
}

func githubHandlerRouter(h *DataSourceHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID, ok := c.Request.Context().Value(types.TenantIDContextKey).(uint64); ok {
			c.Set(types.TenantIDContextKey.String(), tenantID)
		}
		c.Next()
	})
	r.POST("/datasource/github/discover", h.DiscoverGitHubRepositories)
	r.POST("/datasource/github/batch", h.CreateGitHubBatch)
	return r
}

func TestDataSourceGitHubDiscoverUsesRequestOnlyCredentials(t *testing.T) {
	var received *types.GitHubDiscoveryRequest
	service := &githubDataSourceServiceStub{discover: func(_ context.Context, req *types.GitHubDiscoveryRequest) (*types.GitHubDiscoveryResponse, error) {
		received = req
		return &types.GitHubDiscoveryResponse{Owner: req.Owner, Repositories: []types.GitHubRepositoryCandidate{{Repository: "owner/repo", DefaultBranch: "main"}}}, nil
	}}
	h := NewDataSourceHandler(service, &stubKBServiceForDS{})
	req := httptest.NewRequest(http.MethodPost, "/datasource/github/discover", strings.NewReader(`{"owner":"owner","credentials":{"access_token":"private-value"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	githubHandlerRouter(h).ServeHTTP(w, withDSCtx(req, 7))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, received)
	require.Equal(t, uint64(7), received.TenantID)
	require.Equal(t, "private-value", received.Credentials["access_token"])
	require.NotContains(t, w.Body.String(), "private-value")
}

func TestDataSourceGitHubBatchEnforcesOwnedKnowledgeBase(t *testing.T) {
	called := false
	service := &githubDataSourceServiceStub{batch: func(_ context.Context, req *types.GitHubBatchRequest) (*types.GitHubBatchResponse, error) {
		called = true
		require.Equal(t, uint64(7), req.TenantID)
		require.Equal(t, "manual", req.SyncPolicy)
		require.Empty(t, req.SyncSchedule)
		require.False(t, req.StartSync)
		return &types.GitHubBatchResponse{Owner: req.Owner, Results: []types.GitHubBatchItemResult{{Repository: "owner/repo", Status: "created", DataSourceID: "ds"}}}, nil
	}}
	kb := &stubKBServiceForDS{getByID: func(_ context.Context, id string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
	}}
	h := NewDataSourceHandler(service, kb)
	body := `{"knowledge_base_id":"kb","owner":"owner","mode":"source","sync_policy":"manual","repositories":[{"repository":"owner/repo"}]}`
	req := httptest.NewRequest(http.MethodPost, "/datasource/github/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	githubHandlerRouter(h).ServeHTTP(w, withDSCtx(req, 7))
	require.Equal(t, http.StatusCreated, w.Code)
	require.True(t, called)
}
