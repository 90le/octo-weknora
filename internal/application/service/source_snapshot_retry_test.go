package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type retrySourceSnapshotConnector struct {
	datasource.Connector
	fail bool
}

func (c *retrySourceSnapshotConnector) BuildSnapshot(ctx context.Context, _ *types.DataSourceConfig, builder *snapshot.Builder) error {
	if c.fail {
		return errors.New("source folder unavailable")
	}
	return builder.Add(ctx, "src/calc.py", []byte("def calculate_points(value):\n    return value * 7\n"), "", "verified-retry")
}
func TestSourceSnapshotRetrySuccessClearsOnlyItsPreviousError(t *testing.T) {
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "source-retry.db")), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	ctx := context.Background()
	sources := repository.NewDataSourceRepository(db)
	logs := repository.NewSyncLogRepository(db)
	cfg := &types.DataSourceConfig{Type: "github", Settings: map[string]interface{}{"mode": "source"}}
	encoded, err := cfg.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{ID: "source", TenantID: 1, KnowledgeBaseID: "kb", Name: "Source", Type: "github", Config: encoded, Status: types.DataSourceStatusActive}
	require.NoError(t, sources.Create(ctx, ds))
	historical := &types.SyncLog{ID: "historical-failure", TenantID: 1, DataSourceID: ds.ID, Status: types.SyncLogStatusFailed, ErrorMessage: "preserve this independent historical failure"}
	require.NoError(t, logs.Create(ctx, historical))
	current := &types.SyncLog{ID: "retried-run", TenantID: 1, DataSourceID: ds.ID, Status: types.SyncLogStatusRunning}
	require.NoError(t, logs.Create(ctx, current))
	service := &DataSourceService{dsRepo: sources, syncLogRepo: logs}
	connector := &retrySourceSnapshotConnector{fail: true}
	require.Error(t, service.processSourceSnapshot(ctx, ds, cfg, connector, current, false))
	failed, err := logs.FindByID(ctx, current.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failed.Status)
	require.Equal(t, "source folder unavailable", failed.ErrorMessage)
	// Retry from persisted objects, as the native task processor does.
	ds, err = sources.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	connector.fail = false
	require.NoError(t, service.processSourceSnapshot(ctx, ds, cfg, connector, failed, false))
	succeeded, err := logs.FindByID(ctx, current.ID)
	require.NoError(t, err)
	require.Equal(t, current.ID, succeeded.ID)
	require.Equal(t, types.SyncLogStatusSuccess, succeeded.Status)
	require.Empty(t, succeeded.ErrorMessage)
	require.Equal(t, 1, succeeded.ItemsTotal)
	require.Equal(t, 1, succeeded.ItemsCreated)
	require.Zero(t, succeeded.ItemsFailed)
	restored, err := sources.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusActive, restored.Status)
	require.Empty(t, restored.ErrorMessage)
	untouched, err := logs.FindByID(ctx, historical.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, untouched.Status)
	require.Equal(t, historical.ErrorMessage, untouched.ErrorMessage)
}
