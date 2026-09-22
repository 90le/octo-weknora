package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDataSourceRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	return db
}

func TestDataSourceRepositoryUpdateSyncStateClearsErrorMessage(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewDataSourceRepository(db)
	now := time.Now().UTC()
	result := types.JSON(`{"total":0}`)

	ds := &types.DataSource{
		ID:              "ds-1",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Feishu",
		Type:            types.ConnectorTypeFeishu,
		Status:          types.DataSourceStatusError,
		ErrorMessage:    "previous failure",
	}
	require.NoError(t, repo.Create(context.Background(), ds))

	ds.Status = types.DataSourceStatusActive
	ds.ErrorMessage = ""
	ds.LastSyncAt = &now
	ds.LastSyncResult = result
	require.NoError(t, repo.UpdateSyncState(context.Background(), ds))

	var stored types.DataSource
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.Equal(t, types.DataSourceStatusActive, stored.Status)
	assert.Empty(t, stored.ErrorMessage)
	assert.Equal(t, result.ToString(), stored.LastSyncResult.ToString())
	require.NotNil(t, stored.LastSyncAt)
}

func TestDataSourceRepositoryUpdatePersistsDisabledSyncDeletions(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewDataSourceRepository(db)
	ctx := context.Background()

	ds := &types.DataSource{
		ID:              "ds-sync-deletions",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Feishu",
		Type:            types.ConnectorTypeFeishu,
		SyncDeletions:   true,
	}
	require.NoError(t, repo.Create(ctx, ds))

	ds.SyncDeletions = false
	require.NoError(t, repo.Update(ctx, ds))
	assert.False(t, ds.SyncDeletions)

	var stored types.DataSource
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.False(t, stored.SyncDeletions)

	ds.SyncDeletions = true
	require.NoError(t, repo.Update(ctx, ds))
	assert.True(t, ds.SyncDeletions)
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.True(t, stored.SyncDeletions)
}

func TestDataSourceRepositoryCreatePersistsDisabledSyncDeletions(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewDataSourceRepository(db)
	ctx := context.Background()

	ds := &types.DataSource{
		ID:              "ds-create-sync-deletions",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Feishu",
		Type:            types.ConnectorTypeFeishu,
		SyncDeletions:   false,
	}
	require.NoError(t, repo.Create(ctx, ds))
	assert.False(t, ds.SyncDeletions, "Create must not leave the in-memory field hydrated to the GORM default")

	var stored types.DataSource
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.False(t, stored.SyncDeletions)

	// A later Updates() on the same pointer must not persist the hydrated default.
	ds.Name = "Renamed"
	require.NoError(t, repo.Update(ctx, ds))
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.False(t, stored.SyncDeletions)
	assert.Equal(t, "Renamed", stored.Name)
}

func TestDataSourceRepositoryCreatePersistsEnabledSyncDeletions(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewDataSourceRepository(db)
	ctx := context.Background()

	ds := &types.DataSource{
		ID:              "ds-create-sync-deletions-enabled",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Feishu",
		Type:            types.ConnectorTypeFeishu,
		SyncDeletions:   true,
	}
	require.NoError(t, repo.Create(ctx, ds))
	assert.True(t, ds.SyncDeletions)

	var stored types.DataSource
	require.NoError(t, db.First(&stored, "id = ?", ds.ID).Error)
	assert.True(t, stored.SyncDeletions)
}

func TestDataSourceRepositoryDeleteSoftDeletesOnSQLite(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewDataSourceRepository(db)
	ctx := context.Background()

	target := &types.DataSource{
		ID:              "ds-delete-target",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Delete target",
		Type:            types.ConnectorTypeFeishu,
	}
	other := &types.DataSource{
		ID:              "ds-delete-other",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		Name:            "Other data source",
		Type:            types.ConnectorTypeFeishu,
	}
	require.NoError(t, repo.Create(ctx, target))
	require.NoError(t, repo.Create(ctx, other))

	require.NoError(t, repo.Delete(ctx, target.ID))

	var deleted types.DataSource
	require.NoError(t, db.Unscoped().First(&deleted, "id = ?", target.ID).Error)
	assert.True(t, deleted.DeletedAt.Valid)

	found, err := repo.FindByID(ctx, target.ID)
	assert.Error(t, err)
	assert.Nil(t, found)

	untouched, err := repo.FindByID(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, other.ID, untouched.ID)
}

func TestSyncLogRepositoryUpdateResultClearsErrorMessage(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewSyncLogRepository(db)
	finishedAt := time.Now().UTC()
	result := types.JSON(`{"total":0}`)

	log := &types.SyncLog{
		ID:           "log-1",
		DataSourceID: "ds-1",
		TenantID:     1,
		Status:       types.SyncLogStatusFailed,
		ErrorMessage: "previous failure",
		ItemsTotal:   1,
		ItemsFailed:  1,
	}
	require.NoError(t, repo.Create(context.Background(), log))

	log.Status = types.SyncLogStatusSuccess
	log.ErrorMessage = ""
	log.FinishedAt = &finishedAt
	log.ItemsTotal = 0
	log.ItemsFailed = 0
	log.Result = result
	require.NoError(t, repo.UpdateResult(context.Background(), log))

	var stored types.SyncLog
	require.NoError(t, db.First(&stored, "id = ?", log.ID).Error)
	assert.Equal(t, types.SyncLogStatusSuccess, stored.Status)
	assert.Empty(t, stored.ErrorMessage)
	assert.Zero(t, stored.ItemsTotal)
	assert.Zero(t, stored.ItemsFailed)
	assert.Equal(t, result.ToString(), stored.Result.ToString())
	require.NotNil(t, stored.FinishedAt)
}

func TestSyncLogRepositoryUpdateResultIfRunningDoesNotOverwriteTerminalState(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewSyncLogRepository(db)
	log := &types.SyncLog{
		ID:           "log-terminal-cas",
		DataSourceID: "ds-1",
		TenantID:     1,
		Status:       types.SyncLogStatusCanceled,
		ErrorMessage: "operator cancelled this run",
	}
	require.NoError(t, repo.Create(context.Background(), log))

	stale := *log
	stale.Status = types.SyncLogStatusSuccess
	stale.ErrorMessage = ""
	applied, err := repo.UpdateResultIfRunning(context.Background(), &stale)
	require.NoError(t, err)
	assert.False(t, applied)

	var stored types.SyncLog
	require.NoError(t, db.First(&stored, "id = ?", log.ID).Error)
	assert.Equal(t, types.SyncLogStatusCanceled, stored.Status)
	assert.Equal(t, "operator cancelled this run", stored.ErrorMessage)
}

func TestSyncRunOutcomeTransactionRollsBackLogWhenDataSourceStateFails(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewSyncLogRepository(db)
	finalizer, ok := repo.(interface {
		UpdateResultAndDataSourceIfRunning(context.Context, *types.SyncLog, *types.DataSource) (bool, error)
	})
	require.True(t, ok)

	ds := &types.DataSource{
		ID: "ds-outcome-tx", TenantID: 1, KnowledgeBaseID: "kb-1", Name: "Source",
		Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusActive,
	}
	require.NoError(t, db.Create(ds).Error)
	log := &types.SyncLog{
		ID: "log-outcome-tx", DataSourceID: ds.ID, TenantID: ds.TenantID,
		Status: types.SyncLogStatusRunning,
	}
	require.NoError(t, repo.Create(context.Background(), log))
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_sync_state
		BEFORE UPDATE OF last_sync_at ON data_sources
		WHEN NEW.id = 'ds-outcome-tx'
		BEGIN
			SELECT RAISE(FAIL, 'forced datasource state failure');
		END;
	`).Error)

	now := time.Now().UTC()
	ds.LastSyncAt = &now
	ds.LastSyncResult = types.JSON(`{"created":1}`)
	log.Status = types.SyncLogStatusSuccess
	log.FinishedAt = &now
	log.Result = types.JSON(`{"created":1}`)
	applied, err := finalizer.UpdateResultAndDataSourceIfRunning(context.Background(), log, ds)
	require.ErrorContains(t, err, "forced datasource state failure")
	assert.False(t, applied)

	var storedLog types.SyncLog
	require.NoError(t, db.First(&storedLog, "id = ?", log.ID).Error)
	assert.Equal(t, types.SyncLogStatusRunning, storedLog.Status)
	assert.Nil(t, storedLog.FinishedAt)
	var storedDS types.DataSource
	require.NoError(t, db.First(&storedDS, "id = ?", ds.ID).Error)
	assert.Nil(t, storedDS.LastSyncAt)
	assert.Empty(t, storedDS.LastSyncResult)
}

func TestSyncRunOutcomeTransactionRollsBackDataSourceWhenLogIsTerminal(t *testing.T) {
	db := setupDataSourceRepoTestDB(t)
	repo := NewSyncLogRepository(db)
	finalizer, ok := repo.(interface {
		UpdateResultAndDataSourceIfRunning(context.Context, *types.SyncLog, *types.DataSource) (bool, error)
	})
	require.True(t, ok)

	ds := &types.DataSource{
		ID: "ds-terminal-tx", TenantID: 1, KnowledgeBaseID: "kb-1", Name: "Source",
		Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusError, ErrorMessage: "previous state",
	}
	require.NoError(t, db.Create(ds).Error)
	log := &types.SyncLog{
		ID: "log-terminal-tx", DataSourceID: ds.ID, TenantID: ds.TenantID,
		Status: types.SyncLogStatusCanceled, ErrorMessage: "source deleted",
	}
	require.NoError(t, repo.Create(context.Background(), log))

	now := time.Now().UTC()
	ds.Status = types.DataSourceStatusActive
	ds.ErrorMessage = ""
	ds.LastSyncAt = &now
	ds.LastSyncResult = types.JSON(`{"created":1}`)
	stale := *log
	stale.Status = types.SyncLogStatusSuccess
	stale.FinishedAt = &now
	stale.Result = types.JSON(`{"created":1}`)
	applied, err := finalizer.UpdateResultAndDataSourceIfRunning(context.Background(), &stale, ds)
	require.NoError(t, err)
	assert.False(t, applied)

	var storedLog types.SyncLog
	require.NoError(t, db.First(&storedLog, "id = ?", log.ID).Error)
	assert.Equal(t, types.SyncLogStatusCanceled, storedLog.Status)
	var storedDS types.DataSource
	require.NoError(t, db.First(&storedDS, "id = ?", ds.ID).Error)
	assert.Equal(t, types.DataSourceStatusError, storedDS.Status)
	assert.Equal(t, "previous state", storedDS.ErrorMessage)
	assert.Nil(t, storedDS.LastSyncAt)
	assert.Empty(t, storedDS.LastSyncResult)
}
