package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type reviewService struct {
	*stubDataSourceService
	confirmed    string
	listedTenant uint64
}

func (s *reviewService) ConfirmLocalDeletionReview(_ context.Context, _ string, digest string) (*types.SyncLog, error) {
	s.confirmed = digest
	return &types.SyncLog{ID: "queued"}, nil
}

func (s *reviewService) ListLocalFileStates(
	ctx context.Context,
	_ string,
	_ string,
	_ int,
) (*types.LocalSourceFilePage, error) {
	s.listedTenant, _ = types.TenantIDFromContext(ctx)
	return &types.LocalSourceFilePage{
		Items: []types.LocalSourceFileState{{Path: "fixture.pdf", Status: "indexed"}},
	}, nil
}

func TestLocalDeletionReviewWorkspaceScopeAndConfirmation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	cursor := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"deletion_review": map[string]interface{}{"digest": digest, "paths": []string{"one.pdf", "two.pptx"}},
		},
	}
	raw, err := cursor.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{
		ID:              "source",
		KnowledgeBaseID: "kb",
		Type:            "local_folder",
		SyncDeletions:   true,
		LastSyncCursor:  raw,
	}
	service := &reviewService{
		stubDataSourceService: &stubDataSourceService{
			getDataSource: func(context.Context, string) (*types.DataSource, error) { return ds, nil },
		},
	}
	kb := &stubKBServiceForDS{getByID: func(context.Context, string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{TenantID: 7}, nil
	}}
	h := NewDataSourceHandler(service, kb)
	request := func(tenant uint64, method string, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(types.TenantIDContextKey.String(), tenant)
		c.Params = gin.Params{{Key: "id", Value: "source"}}
		c.Request = httptest.NewRequest(method, "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if method == http.MethodGet {
			h.LocalDeletionReview(c)
		} else {
			h.ConfirmLocalDeletionReview(c)
		}
		return w
	}
	require.Equal(t, http.StatusForbidden, request(8, http.MethodGet, "").Code)
	require.Equal(t, http.StatusForbidden, request(8, http.MethodPost, `{"digest":"`+digest+`"}`).Code)
	require.Empty(t, service.confirmed)
	w := request(7, http.MethodGet, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "one.pdf")
	require.Equal(t, http.StatusOK, request(7, http.MethodPost, `{"digest":"`+digest+`"}`).Code)
	require.Equal(t, digest, service.confirmed)
}

func TestLocalFileStatesRequireOwningWorkspace(t *testing.T) {
	ds := &types.DataSource{ID: "source", KnowledgeBaseID: "kb", Type: "local_folder"}
	service := &reviewService{
		stubDataSourceService: &stubDataSourceService{
			getDataSource: func(context.Context, string) (*types.DataSource, error) { return ds, nil },
		},
	}
	kb := &stubKBServiceForDS{getByID: func(context.Context, string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{TenantID: 7}, nil
	}}
	h := NewDataSourceHandler(service, kb)
	request := func(tenant uint64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set(types.TenantIDContextKey.String(), tenant)
		c.Params = gin.Params{{Key: "id", Value: "source"}}
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		h.LocalFileStates(c)
		return w
	}
	require.Equal(t, http.StatusForbidden, request(8).Code)
	require.Zero(t, service.listedTenant)
	w := request(7)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "fixture.pdf")
	require.EqualValues(t, 7, service.listedTenant)
}
