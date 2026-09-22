package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRestartRecoveryLeaseUsesCompareAndSetOwnership(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.DataSourceRestartRecoveryRun{}))
	repo := NewDataSourceRepository(db).(*DataSourceRepository)
	ds := &types.DataSource{ID: "ds", TenantID: 7, KnowledgeBaseID: "kb", Name: "GitHub", Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive}
	require.NoError(t, repo.Create(context.Background(), ds))

	first, err := repo.TryAcquireRestartRecoveryLease(context.Background(), ds.ID, "run-a", time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, first)
	second, err := repo.TryAcquireRestartRecoveryLease(context.Background(), ds.ID, "run-b", time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, second, "a second worker must not acquire an active source lease")
	renewed, err := repo.RenewRestartRecoveryLease(context.Background(), ds.ID, "run-a", time.Now().UTC().Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, renewed)
	require.NoError(t, repo.ReleaseRestartRecoveryLease(context.Background(), ds.ID, "run-a"))
	afterRelease, err := repo.TryAcquireRestartRecoveryLease(context.Background(), ds.ID, "run-b", time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, afterRelease)
}
