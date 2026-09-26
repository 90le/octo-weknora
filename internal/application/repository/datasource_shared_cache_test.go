package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFindGitHubByTenantIncludesPausedAndErrorSubscribers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "subscribers.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}))
	repo := &DataSourceRepository{db: db}
	for _, item := range []*types.DataSource{
		{ID: "active", TenantID: 7, KnowledgeBaseID: "kb-a", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive},
		{ID: "paused", TenantID: 7, KnowledgeBaseID: "kb-b", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusPaused},
		{ID: "error", TenantID: 7, KnowledgeBaseID: "kb-c", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusError},
		{ID: "deleted", TenantID: 7, KnowledgeBaseID: "kb-d", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive},
		{ID: "other-tenant", TenantID: 8, KnowledgeBaseID: "kb-e", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive},
		{ID: "other-type", TenantID: 7, KnowledgeBaseID: "kb-f", Type: types.ConnectorTypeRSS, Status: types.DataSourceStatusActive},
	} {
		require.NoError(t, repo.Create(context.Background(), item))
	}
	require.NoError(t, repo.Delete(context.Background(), "deleted"))
	rows, err := repo.FindGitHubByTenant(context.Background(), 7)
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	require.ElementsMatch(t, []string{"active", "paused", "error"}, ids)
}
