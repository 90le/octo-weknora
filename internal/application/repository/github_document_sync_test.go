package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func githubDocumentTestRows(t *testing.T, db *gorm.DB) (*DataSourceRepository, *types.DataSource, *types.GitHubDocumentRun, []types.GitHubDocumentSyncItem) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.DataSource{}, &types.SyncLog{}, &types.GitHubDocumentRun{}, &types.GitHubDocumentSyncItem{}))
	repo := NewDataSourceRepository(db).(*DataSourceRepository)
	id := uuid.NewString()
	ds := &types.DataSource{
		ID: id, TenantID: 7, KnowledgeBaseID: "kb-" + id, Type: types.ConnectorTypeGitHub,
		Status: types.DataSourceStatusActive, SyncDeletions: true,
		Config:         types.JSON(`{"type":"github","settings":{"repository":"test/docs"}}`),
		LastSyncCursor: types.JSON(`{"connector_cursor":{"selection":"old","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","files":{}}}`),
	}
	require.NoError(t, db.Create(&types.KnowledgeBase{ID: ds.KnowledgeBaseID, TenantID: ds.TenantID, Name: "GitHub test KB", Type: types.KnowledgeBaseTypeDocument}).Error)
	require.NoError(t, repo.Create(context.Background(), ds))
	// DataSourceRepository.Create explicitly persists the false/true flag.
	stored, err := repo.FindByID(context.Background(), id)
	require.NoError(t, err)
	ds = stored
	require.NoError(t, db.Create(&types.SyncLog{ID: "log-" + ds.ID, DataSourceID: ds.ID, TenantID: ds.TenantID, Status: types.SyncLogStatusRunning}).Error)
	run := &types.GitHubDocumentRun{
		ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: ds.KnowledgeBaseID, DataSourceID: ds.ID,
		Selection: strings.Repeat("b", 64), CommitSHA: strings.Repeat("c", 40), PlanDigest: strings.Repeat("d", 64),
		CredentialScope: "public", TargetCursor: types.JSON(`{"connector_cursor":{"selection":"new","commit":"cccccccccccccccccccccccccccccccccccccccc","files":{"guide.md":{}}}}`),
		Status: types.GitHubDocumentRunRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	items := []types.GitHubDocumentSyncItem{
		{RunID: run.ID, PathHash: strings.Repeat("1", 64), Path: "guide.md", BlobSHA: strings.Repeat("e", 40), Operation: types.GitHubDocumentItemUpsert, Status: types.GitHubDocumentItemPending},
		{RunID: run.ID, PathHash: strings.Repeat("2", 64), Path: "removed.md", Operation: types.GitHubDocumentItemDelete, Status: types.GitHubDocumentItemPending},
	}
	require.NoError(t, repo.CreateGitHubDocumentRun(context.Background(), run, items))
	return repo, ds, run, items
}

func TestGitHubDocumentProgressSQLiteAckAndCursorRollback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "github-progress.db")), &gorm.Config{})
	require.NoError(t, err)
	repo, ds, run, items := githubDocumentTestRows(t, db)
	ctx := context.Background()
	wrong, err := repo.FindRunningGitHubDocumentRun(ctx, ds.TenantID, "other-kb", ds.ID)
	require.NoError(t, err)
	require.Nil(t, wrong)
	found, err := repo.FindRunningGitHubDocumentRun(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, found.ID)
	leaseID := uuid.NewString()
	ok, err := repo.AcquireGitHubDocumentRunLease(ctx, run, leaseID, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.AcquireGitHubDocumentRunLease(ctx, run, uuid.NewString(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, repo.UpdateGitHubDocumentItem(ctx, run, leaseID, items[0], types.GitHubDocumentItemReady, types.GitHubDocumentOutcomeCreated, ""))
	require.ErrorIs(t, repo.PublishGitHubDocumentRun(ctx, run, leaseID, ds, "log-"+ds.ID), ErrGitHubDocumentRunChanged)
	stored, err := repo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(stored.LastSyncCursor), `"selection":"old"`, "failed deletion must roll back the complete cursor")
	require.NoError(t, repo.UpdateGitHubDocumentItem(ctx, run, leaseID, items[1], types.GitHubDocumentItemFailed, "", "deletion_failed"))
	require.ErrorIs(t, repo.PublishGitHubDocumentRun(ctx, run, leaseID, ds, "log-"+ds.ID), ErrGitHubDocumentRunChanged)
	var runAfterFailure types.GitHubDocumentRun
	require.NoError(t, db.First(&runAfterFailure, "id = ?", run.ID).Error)
	require.Equal(t, types.GitHubDocumentRunRunning, runAfterFailure.Status)
	require.Equal(t, leaseID, runAfterFailure.LeaseID)
	require.NotNil(t, runAfterFailure.LeaseUntil)
	require.True(t, runAfterFailure.LeaseUntil.After(time.Now()), "failed publish must retain the live lease")
	var deletionAfterFailure types.GitHubDocumentSyncItem
	require.NoError(t, db.First(&deletionAfterFailure, "run_id = ? AND path_hash = ?", run.ID, items[1].PathHash).Error)
	require.Equal(t, types.GitHubDocumentItemFailed, deletionAfterFailure.Status)
	require.NoError(t, repo.UpdateGitHubDocumentItem(ctx, run, leaseID, items[1], types.GitHubDocumentItemReady, types.GitHubDocumentOutcomeDeleted, ""))
	require.NoError(t, repo.PublishGitHubDocumentRun(ctx, run, leaseID, ds, "log-"+ds.ID))
	stored, err = repo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(stored.LastSyncCursor), `"selection":"new"`)
	require.NotNil(t, stored.LastSyncAt)
	active, err := repo.FindRunningGitHubDocumentRun(ctx, ds.TenantID, ds.KnowledgeBaseID, ds.ID)
	require.NoError(t, err)
	require.Nil(t, active)
}

func TestGitHubDocumentProgressSQLiteRejectsCredentialChangeBeforePublish(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "github-revoke.db")), &gorm.Config{})
	require.NoError(t, err)
	repo, ds, run, items := githubDocumentTestRows(t, db)
	ctx := context.Background()
	leaseID := uuid.NewString()
	ok, err := repo.AcquireGitHubDocumentRunLease(ctx, run, leaseID, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	for _, item := range items {
		require.NoError(t, repo.UpdateGitHubDocumentItem(ctx, run, leaseID, item, types.GitHubDocumentItemReady, types.GitHubDocumentOutcomeSkipped, ""))
	}
	// The sync worker still holds the original encrypted config snapshot. A
	// credential rotation or source edit must invalidate its cursor publish.
	require.NoError(t, db.Model(&types.DataSource{}).Where("id = ?", ds.ID).
		Update("config", types.JSON(`{"type":"github","credentials":{"access_token":"rotated"},"settings":{"repository":"test/docs"}}`)).Error)
	require.ErrorIs(t, repo.PublishGitHubDocumentRun(ctx, run, leaseID, ds, "log-"+ds.ID), ErrGitHubDocumentRunChanged)
	stored, err := repo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(stored.LastSyncCursor), `"selection":"old"`)
}

func TestGitHubDocumentProgressSQLiteCanceledRunCannotPublish(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "github-cancel.db")), &gorm.Config{})
	require.NoError(t, err)
	repo, ds, run, items := githubDocumentTestRows(t, db)
	ctx := context.Background()
	leaseID := uuid.NewString()
	ok, err := repo.AcquireGitHubDocumentRunLease(ctx, run, leaseID, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	for _, item := range items {
		require.NoError(t, repo.UpdateGitHubDocumentItem(ctx, run, leaseID, item,
			types.GitHubDocumentItemReady, types.GitHubDocumentOutcomeSkipped, ""))
	}
	require.NoError(t, db.Model(&types.SyncLog{}).Where("id = ?", "log-"+ds.ID).
		Update("status", types.SyncLogStatusCanceled).Error)
	require.ErrorIs(t, repo.PublishGitHubDocumentRun(ctx, run, leaseID, ds, "log-"+ds.ID), ErrGitHubDocumentRunChanged)
	stored, err := repo.FindByID(ctx, ds.ID)
	require.NoError(t, err)
	require.Contains(t, string(stored.LastSyncCursor), `"selection":"old"`)
}

func TestGitHubDocumentCheckpointPostgresSchemaAndRollback(t *testing.T) {
	dsn := os.Getenv("WEKNORA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	var name string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&name).Error)
	require.True(t, strings.HasPrefix(name, "weknora_test_"), "refusing to mutate non-test database %q", name)
	require.NoError(t, db.AutoMigrate(&types.KnowledgeBase{}, &types.DataSource{}))
	up, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000106_github_document_checkpoint.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(up)).Error)
	t.Cleanup(func() {
		down, readErr := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000106_github_document_checkpoint.down.sql"))
		if readErr == nil {
			_ = db.Exec(string(down)).Error
		}
	})
	repo, ds, run, items := githubDocumentTestRows(t, db)
	t.Cleanup(func() { _ = db.Unscoped().Where("id = ?", ds.ID).Delete(&types.DataSource{}).Error })
	leaseID := uuid.NewString()
	ok, err := repo.AcquireGitHubDocumentRunLease(context.Background(), run, leaseID, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	for _, item := range items {
		require.NoError(t, repo.UpdateGitHubDocumentItem(context.Background(), run, leaseID, item, types.GitHubDocumentItemReady, types.GitHubDocumentOutcomeSkipped, ""))
	}
	require.NoError(t, repo.PublishGitHubDocumentRun(context.Background(), run, leaseID, ds, "log-"+ds.ID))
}
