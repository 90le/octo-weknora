package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dataSourceDeleteServiceStub struct {
	interfaces.DataSourceService
	preview func(context.Context, string) (*types.DataSourceDeletePreview, error)
	delete  func(context.Context, string, *types.DataSourceDeleteRequest) (*types.DataSourceDeleteResult, error)
}

func (s *dataSourceDeleteServiceStub) GetDataSource(_ context.Context, id string) (*types.DataSource, error) {
	return &types.DataSource{ID: id, TenantID: 7, KnowledgeBaseID: "kb-7"}, nil
}

func (s *dataSourceDeleteServiceStub) PreviewDataSourceDelete(ctx context.Context, id string) (*types.DataSourceDeletePreview, error) {
	return s.preview(ctx, id)
}

func (s *dataSourceDeleteServiceStub) DeleteDataSourceWithMode(ctx context.Context, id string, req *types.DataSourceDeleteRequest) (*types.DataSourceDeleteResult, error) {
	return s.delete(ctx, id, req)
}

func dataSourceDeleteHandlerRouter(h *DataSourceHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID, ok := c.Request.Context().Value(types.TenantIDContextKey).(uint64); ok {
			c.Set(types.TenantIDContextKey.String(), tenantID)
		}
		c.Next()
	})
	r.POST("/datasource/:id/delete-preview", h.PreviewDataSourceDelete)
	r.POST("/datasource/:id/delete", h.DeleteDataSourceWithMode)
	return r
}

func TestDataSourceDeletePreviewAndPurgeHandlerContract(t *testing.T) {
	var received *types.DataSourceDeleteRequest
	service := &dataSourceDeleteServiceStub{
		preview: func(_ context.Context, id string) (*types.DataSourceDeletePreview, error) {
			return &types.DataSourceDeletePreview{
				DataSourceID: id, GeneratedKnowledgeCount: 2, GeneratedStorageBytes: 42,
				SharedOrUnverifiableResourcesCount: 1, PreviewToken: "opaque-preview-token",
			}, nil
		},
		delete: func(_ context.Context, id string, req *types.DataSourceDeleteRequest) (*types.DataSourceDeleteResult, error) {
			received = req
			return &types.DataSourceDeleteResult{DataSourceID: id, Mode: req.Mode, PurgedKnowledge: 2, DataSourceDeleted: true}, nil
		},
	}
	kb := &stubKBServiceForDS{getByID: func(_ context.Context, id string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
	}}
	r := dataSourceDeleteHandlerRouter(NewDataSourceHandler(service, kb))

	previewReq := httptest.NewRequest(http.MethodPost, "/datasource/ds-7/delete-preview", nil)
	previewRes := httptest.NewRecorder()
	r.ServeHTTP(previewRes, withDSCtx(previewReq, 7))
	require.Equal(t, http.StatusOK, previewRes.Code)
	assert.NotContains(t, previewRes.Body.String(), "local://")
	assert.Contains(t, previewRes.Body.String(), "opaque-preview-token")

	purgeReq := httptest.NewRequest(http.MethodPost, "/datasource/ds-7/delete", strings.NewReader(`{"mode":"purge_generated","preview_token":"opaque-preview-token"}`))
	purgeReq.Header.Set("Content-Type", "application/json")
	purgeRes := httptest.NewRecorder()
	r.ServeHTTP(purgeRes, withDSCtx(purgeReq, 7))
	require.Equal(t, http.StatusOK, purgeRes.Code)
	require.NotNil(t, received)
	assert.Equal(t, types.DataSourceDeleteModePurgeGenerated, received.Mode)
	assert.Equal(t, "opaque-preview-token", received.PreviewToken)
}

func TestDataSourceDeleteHandlerPreservesPreviewConflict(t *testing.T) {
	service := &dataSourceDeleteServiceStub{
		preview: func(_ context.Context, _ string) (*types.DataSourceDeletePreview, error) { return nil, nil },
		delete: func(_ context.Context, _ string, _ *types.DataSourceDeleteRequest) (*types.DataSourceDeleteResult, error) {
			return nil, apperrors.NewConflictError("data source content changed; request a new delete preview")
		},
	}
	kb := &stubKBServiceForDS{getByID: func(_ context.Context, id string) (*types.KnowledgeBase, error) {
		return &types.KnowledgeBase{ID: id, TenantID: 7}, nil
	}}
	r := dataSourceDeleteHandlerRouter(NewDataSourceHandler(service, kb))
	req := httptest.NewRequest(http.MethodPost, "/datasource/ds-7/delete", strings.NewReader(`{"mode":"purge_generated","preview_token":"stale"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, withDSCtx(req, 7))
	require.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "request a new delete preview")
}
