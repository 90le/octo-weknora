package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type syncGuardFixture struct {
	db       *gorm.DB
	svc      *DataSourceService
	ctx      context.Context
	ds       *types.DataSource
	cfg      *types.DataSourceConfig
	log      *types.SyncLog
	roots    *localfolder.Registry
	local    *localfolder.Connector
	filename string
}

func newSyncGuardFixture(t *testing.T) *syncGuardFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "guard.db")), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	migration, err := os.ReadFile("../../../migrations/sqlite/000020_local_source_roots.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	input := t.TempDir()
	filename := filepath.Join(input, "README.md")
	require.NoError(t, os.WriteFile(filename, []byte("previous verified content\n"), 0o600))
	legacy, _ := json.Marshal([]map[string]any{{"id": "root", "name": "Product source", "path": input, "tenant_id": 1}})
	t.Setenv("DATASOURCE_LOCAL_SPACES", "")
	t.Setenv("DATASOURCE_LOCAL_ROOTS", string(legacy))
	t.Setenv("DATASOURCE_SNAPSHOT_DIR", t.TempDir())
	roots, err := localfolder.NewRegistry(db)
	require.NoError(t, err)
	connector := localfolder.NewConnector(roots)
	connectors := datasource.NewConnectorRegistry()
	require.NoError(t, connectors.Register(connector))
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"root_id": "root", "mode": "source"}}
	encoded, err := cfg.ToJSON()
	require.NoError(t, err)
	ds := &types.DataSource{ID: "source", TenantID: 1, KnowledgeBaseID: "kb", Type: localfolder.Type, Status: types.DataSourceStatusActive, Config: encoded}
	logs := repository.NewSyncLogRepository(db)
	sources := repository.NewDataSourceRepository(db)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	require.NoError(t, sources.Create(ctx, ds))
	log := &types.SyncLog{ID: "run", TenantID: 1, DataSourceID: ds.ID, Status: types.SyncLogStatusRunning}
	require.NoError(t, logs.Create(ctx, log))
	return &syncGuardFixture{db: db, ctx: ctx, ds: ds, cfg: cfg, log: log, roots: roots, local: connector, filename: filename,
		svc: &DataSourceService{dsRepo: sources, syncLogRepo: logs, connectorRegistry: connectors}}
}

type blockingSnapshotConnector struct {
	datasource.Connector
	provider datasource.SnapshotConnector
	fetched  chan struct{}
	release  chan struct{}
}

func (c *blockingSnapshotConnector) BuildSnapshot(ctx context.Context, cfg *types.DataSourceConfig, b *snapshot.Builder) error {
	if err := c.provider.BuildSnapshot(ctx, cfg, b); err != nil {
		return err
	}
	close(c.fetched)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSourceSnapshotConcurrentRevocationNeverAdvancesVersion(t *testing.T) {
	for _, action := range []string{"root_disabled", "source_paused", "source_deleted", "selection_changed"} {
		t.Run(action, func(t *testing.T) {
			f := newSyncGuardFixture(t)
			require.NoError(t, f.svc.processSourceSnapshot(f.ctx, f.ds, f.cfg, f.local, f.log, false))
			previous := append(types.JSON(nil), f.ds.LastSyncCursor...)
			cursor, err := f.ds.ParseSyncCursor()
			require.NoError(t, err)
			oldVersion := cursor.ConnectorCursor["snapshot_id"].(string)
			require.NoError(t, os.WriteFile(f.filename, []byte("uncommitted fetched content\n"), 0o600))
			blocked := &blockingSnapshotConnector{provider: f.local, fetched: make(chan struct{}), release: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- f.svc.processSourceSnapshot(f.ctx, f.ds, f.cfg, blocked, f.log, false) }()
			select {
			case <-blocked.fetched:
			case <-time.After(5 * time.Second):
				t.Fatal("source did not reach the controlled pre-commit boundary")
			}
			switch action {
			case "root_disabled":
				_, err = f.roots.Update(f.ctx, 1, "root", "Product source", false)
			case "source_paused":
				err = f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusPaused).Error
			case "source_deleted":
				err = f.svc.dsRepo.Delete(f.ctx, f.ds.ID)
			case "selection_changed":
				changed, e := (&types.DataSourceConfig{Settings: map[string]interface{}{"root_id": "root", "mode": "source", "directory": "other"}}).ToJSON()
				require.NoError(t, e)
				err = f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("config", changed).Error
			}
			require.NoError(t, err)
			close(blocked.release)
			require.ErrorIs(t, <-done, asynq.SkipRetry)
			storedLog, err := f.svc.syncLogRepo.FindByID(f.ctx, f.log.ID)
			require.NoError(t, err)
			require.Equal(t, types.SyncLogStatusCanceled, storedLog.Status)
			store, err := snapshot.FromEnvironment()
			require.NoError(t, err)
			_, cacheErr := store.Load(f.ds, f.cfg, oldVersion)
			if action == "source_deleted" {
				require.Error(t, cacheErr, "deleted sources must not recreate an accessible cache")
			} else {
				require.NoError(t, cacheErr, "revocation is not deletion of the last good version")
				stored, e := f.svc.dsRepo.FindByID(f.ctx, f.ds.ID)
				require.NoError(t, e)
				require.Equal(t, previous, stored.LastSyncCursor)
				if action == "source_paused" {
					require.Equal(t, types.DataSourceStatusPaused, stored.Status)
				}
			}
			body, err := os.ReadFile(f.filename)
			require.NoError(t, err)
			require.Equal(t, "uncommitted fetched content\n", string(body), "operator files are never altered")
		})
	}
}

type fetchedMutationConnector struct {
	deletedItemConnector
	mutate func()
	items  []types.FetchedItem
}

func (c fetchedMutationConnector) FetchAll(ctx context.Context, cfg *types.DataSourceConfig, ids []string) ([]types.FetchedItem, error) {
	items, err := c.deletedItemConnector.FetchAll(ctx, cfg, ids)
	if c.items != nil {
		items = c.items
	}
	if c.mutate != nil {
		c.mutate()
	}
	return items, err
}

func TestProcessSyncRechecksFetchedBatchBeforeAnyKnowledgeMutation(t *testing.T) {
	for _, action := range []string{"pause", "delete"} {
		t.Run(action, func(t *testing.T) {
			h := newSyncDeletionHarness(t, true, "ds", "run", nil, nil)
			connectors := datasource.NewConnectorRegistry()
			require.NoError(t, connectors.Register(fetchedMutationConnector{mutate: func() {
				if action == "delete" {
					require.NoError(t, h.svc.dsRepo.Delete(context.Background(), h.ds.ID))
				} else {
					h.ds.Status = types.DataSourceStatusPaused
				}
			}}))
			h.svc.connectorRegistry = connectors
			log, err := h.run(t)
			require.ErrorIs(t, err, asynq.SkipRetry)
			require.Equal(t, types.SyncLogStatusCanceled, log.Status)
			require.Empty(t, h.knowledgeSvc.deleted)
			require.Empty(t, h.knowledgeRepo.hardDeleted)
			if action == "pause" {
				require.Equal(t, types.DataSourceStatusPaused, h.ds.Status)
			}
		})
	}
}

func TestSyncGuardKeepsManualPausedSyncAndRejectsForeignRoot(t *testing.T) {
	f := newSyncGuardFixture(t)
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusPaused).Error)
	guard := &syncAccessGuard{svc: f.svc, ds: f.ds, config: f.cfg}
	require.ErrorIs(t, guard.check(f.ctx), errSyncAccessChanged)
	guard.allowPaused = true
	require.NoError(t, guard.check(f.ctx))
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusActive).Error)
	require.ErrorIs(t, guard.check(f.ctx), errSyncAccessChanged, "an older manual run must not overwrite a subsequent resume")
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusPaused).Error)
	// The same physical root ID in another tenant must not become an implicit grant.
	require.NoError(t, f.db.Model(&localfolder.Root{}).Where("id = ?", "root").Update("tenant_id", 2).Error)
	require.ErrorIs(t, guard.check(f.ctx), errSyncAccessChanged)
}

type batchRevokingKnowledgeService struct {
	*sweepFakeKS
	revoke func()
}

func (s *batchRevokingKnowledgeService) DeleteKnowledge(ctx context.Context, id string) error {
	err := s.sweepFakeKS.DeleteKnowledge(ctx, id)
	if len(s.deleted) == syncAccessBatchSize {
		s.revoke()
	}
	return err
}

func TestProcessSyncStopsBeforeNextBoundedBatchAfterPause(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds", "run", nil, nil)
	items := make([]types.FetchedItem, 2*syncAccessBatchSize+1)
	for i := range items {
		items[i] = types.FetchedItem{ExternalID: "known-document", IsDeleted: true}
	}
	connectors := datasource.NewConnectorRegistry()
	require.NoError(t, connectors.Register(fetchedMutationConnector{items: items}))
	h.svc.connectorRegistry = connectors
	h.svc.knowledgeService = &batchRevokingKnowledgeService{sweepFakeKS: h.knowledgeSvc, revoke: func() { h.ds.Status = types.DataSourceStatusPaused }}
	log, err := h.run(t)
	require.ErrorIs(t, err, asynq.SkipRetry)
	require.Equal(t, types.SyncLogStatusCanceled, log.Status)
	require.Equal(t, syncAccessBatchSize, log.ItemsDeleted)
	require.Len(t, h.knowledgeSvc.deleted, syncAccessBatchSize)
	require.Empty(t, h.ds.LastSyncCursor)
	require.Equal(t, types.DataSourceStatusPaused, h.ds.Status)
}

func TestProcessSyncOnlyExplicitManualRunMayStartWhilePaused(t *testing.T) {
	for _, trigger := range []string{"manual", "scheduled", ""} {
		t.Run(trigger, func(t *testing.T) {
			h := newSyncDeletionHarness(t, true, "ds", "run", nil, nil)
			h.ds.Status = types.DataSourceStatusPaused
			payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: h.ds.ID, TenantID: 1, SyncLogID: h.syncLogID, ForceFull: true, Trigger: trigger})
			require.NoError(t, err)
			err = h.svc.ProcessSync(context.Background(), asynq.NewTask(types.TypeDataSourceSync, payload))
			if trigger == "manual" {
				require.NoError(t, err)
				require.Len(t, h.knowledgeSvc.deleted, 1)
			} else {
				require.ErrorIs(t, err, asynq.SkipRetry)
				require.Empty(t, h.knowledgeSvc.deleted)
			}
			require.Equal(t, types.DataSourceStatusPaused, h.ds.Status)
		})
	}
}

func TestStreamCheckpointRechecksSourceAndDoesNotAdvanceCanceledCursor(t *testing.T) {
	f := newSyncGuardFixture(t)
	guard := &syncAccessGuard{svc: f.svc, ds: f.ds, config: f.cfg}
	h := &streamSyncHandler{svc: f.svc, ds: f.ds, syncLog: f.log, result: &types.SyncResult{}, guard: guard}
	require.NoError(t, guard.check(f.ctx))
	_, err := f.roots.Update(f.ctx, 1, "root", "Product source", false)
	require.NoError(t, err)
	err = h.Checkpoint(f.ctx, &types.SyncCursor{ConnectorCursor: map[string]interface{}{"must_not_save": "new"}})
	require.ErrorIs(t, err, errSyncAccessChanged)
	stored, err := f.svc.dsRepo.FindByID(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Empty(t, stored.LastSyncCursor)
}
