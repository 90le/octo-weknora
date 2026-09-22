package service

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/stretchr/testify/assert"
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

type blockingSyncRunConnector struct {
	deletedItemConnector
	started chan struct{}
}

func (c blockingSyncRunConnector) FetchAll(ctx context.Context, _ *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type terminalAfterFetchConnector struct {
	deletedItemConnector
	terminate func()
}

type retryableFetchConnector struct {
	deletedItemConnector
	calls int
}

func (c *retryableFetchConnector) FetchAll(ctx context.Context, cfg *types.DataSourceConfig, ids []string) ([]types.FetchedItem, error) {
	c.calls++
	if c.calls == 1 {
		return nil, errors.New("transient source outage")
	}
	return c.deletedItemConnector.FetchAll(ctx, cfg, ids)
}

type captureStreamingRetryConnector struct {
	deletedItemConnector
	startCursor *types.SyncCursor
}

type deadlineFetchConnector struct {
	deletedItemConnector
}

func (deadlineFetchConnector) FetchAll(ctx context.Context, _ *types.DataSourceConfig, _ []string) ([]types.FetchedItem, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type postCASCancelSyncLogRepo struct {
	*processSyncSyncLogRepo
	cancel context.CancelFunc
}

func (r *postCASCancelSyncLogRepo) UpdateResultIfRunning(ctx context.Context, log *types.SyncLog) (bool, error) {
	applied, err := r.processSyncSyncLogRepo.UpdateResultIfRunning(ctx, log)
	if applied && r.cancel != nil {
		r.cancel()
	}
	return applied, err
}

type postCASStateDSRepo struct {
	*recordingDSRepo
	sawCanceled bool
}

func (r *postCASStateDSRepo) UpdateSyncState(ctx context.Context, ds *types.DataSource) error {
	r.sawCanceled = ctx.Err() != nil
	return r.recordingDSRepo.UpdateSyncState(ctx, ds)
}

func (c *captureStreamingRetryConnector) FetchStream(
	_ context.Context,
	_ *types.DataSourceConfig,
	cursor *types.SyncCursor,
	_ datasource.StreamHandler,
) (*types.SyncCursor, error) {
	c.startCursor = cursor
	return nil, errors.New("transient streaming outage")
}

func (c terminalAfterFetchConnector) FetchAll(ctx context.Context, cfg *types.DataSourceConfig, ids []string) ([]types.FetchedItem, error) {
	c.terminate()
	return c.deletedItemConnector.FetchAll(ctx, cfg, ids)
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
			require.NoError(t, f.svc.processSourceSnapshot(f.ctx, f.ds, f.cfg, f.local, f.log, false, nil))
			previous := append(types.JSON(nil), f.ds.LastSyncCursor...)
			cursor, err := f.ds.ParseSyncCursor()
			require.NoError(t, err)
			oldVersion := cursor.ConnectorCursor["snapshot_id"].(string)
			// The second direct invocation models a separately claimed sync run.
			// A live worker only writes while its log is running.
			f.log.Status = types.SyncLogStatusRunning
			f.log.FinishedAt = nil
			f.log.ErrorMessage = ""
			require.NoError(t, f.svc.syncLogRepo.UpdateResult(f.ctx, f.log))
			require.NoError(t, os.WriteFile(f.filename, []byte("uncommitted fetched content\n"), 0o600))
			blocked := &blockingSnapshotConnector{provider: f.local, fetched: make(chan struct{}), release: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- f.svc.processSourceSnapshot(f.ctx, f.ds, f.cfg, blocked, f.log, false, nil) }()
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

func TestProcessSyncStopsWhenSyncLogBecomesTerminal(t *testing.T) {
	oldInterval := syncRunTerminalPollInterval
	syncRunTerminalPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { syncRunTerminalPollInterval = oldInterval })

	for _, terminal := range []string{types.SyncLogStatusCanceled, types.SyncLogStatusFailed} {
		t.Run(terminal, func(t *testing.T) {
			h := newSyncDeletionHarness(t, true, "ds-terminal-"+terminal, "log-terminal-"+terminal, nil, nil)
			started := make(chan struct{})
			registry := datasource.NewConnectorRegistry()
			require.NoError(t, registry.Register(blockingSyncRunConnector{started: started}))
			h.svc.connectorRegistry = registry

			payload, err := json.Marshal(types.DataSourceSyncPayload{
				DataSourceID: h.ds.ID, TenantID: h.ds.TenantID, SyncLogID: h.syncLogID, ForceFull: true,
			})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() {
				done <- h.svc.ProcessSync(context.Background(), asynq.NewTask(types.TypeDataSourceSync, payload))
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("sync did not enter the blocking fetch")
			}

			h.syncLogRepo.setStatus(h.syncLogID, terminal)
			select {
			case err := <-done:
				require.Error(t, err)
				require.True(t, errors.Is(err, asynq.SkipRetry), "terminal log must stop Lite retries: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("terminal sync log did not cancel the blocking connector")
			}
			stored := h.syncLogRepo.snapshot(h.syncLogID)
			require.NotNil(t, stored)
			require.Equal(t, terminal, stored.Status, "worker must not overwrite a terminal log")
			require.Empty(t, h.knowledgeSvc.deleted)
		})
	}
}

func TestProcessSyncDoesNotMutateFetchedBatchAfterTerminalLog(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds-terminal-after-fetch", "log-terminal-after-fetch", nil, nil)
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(terminalAfterFetchConnector{
		terminate: func() { h.syncLogRepo.setStatus(h.syncLogID, types.SyncLogStatusCanceled) },
	}))
	h.svc.connectorRegistry = registry

	log, err := h.run(t)
	require.Error(t, err)
	require.True(t, errors.Is(err, asynq.SkipRetry))
	require.Equal(t, types.SyncLogStatusCanceled, log.Status)
	require.Empty(t, h.knowledgeSvc.deleted, "a terminal log must block post-fetch item mutation")
}

func TestSyncTaskAttemptRecognizesLiteAttemptMetadata(t *testing.T) {
	_, _, ok := syncTaskAttempt(context.Background())
	require.False(t, ok)
	attempt, maxRetry, ok := syncTaskAttempt(types.WithTaskRetryMetadata(context.Background(), 1, 3))
	require.True(t, ok)
	require.Equal(t, 1, attempt)
	require.Equal(t, 3, maxRetry)
	require.True(t, syncTaskCanRetry(types.WithTaskRetryMetadata(context.Background(), 1, 3)))
	require.False(t, syncTaskCanRetry(types.WithTaskRetryMetadata(context.Background(), 3, 3)))
}

func TestProcessSyncRetryableFailureKeepsLeaseRunning(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds-retry", "log-retry", nil, nil)
	registry := datasource.NewConnectorRegistry()
	connector := &retryableFetchConnector{}
	require.NoError(t, registry.Register(connector))
	h.svc.connectorRegistry = registry
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: h.ds.ID, TenantID: h.ds.TenantID, SyncLogID: h.syncLogID, ForceFull: true,
	})
	require.NoError(t, err)
	firstCtx := types.WithTaskRetryMetadata(context.Background(), 0, 1)
	require.Error(t, h.svc.ProcessSync(firstCtx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	intermediate := h.syncLogRepo.snapshot(h.syncLogID)
	require.NotNil(t, intermediate)
	require.Equal(t, types.SyncLogStatusRunning, intermediate.Status)
	require.Nil(t, intermediate.FinishedAt)

	retryCtx := types.WithTaskRetryMetadata(context.Background(), 1, 1)
	require.NoError(t, h.svc.ProcessSync(retryCtx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	stored := h.syncLogRepo.snapshot(h.syncLogID)
	require.NotNil(t, stored)
	require.Equal(t, types.SyncLogStatusSuccess, stored.Status)
	require.Len(t, h.knowledgeSvc.deleted, 1)
}

func TestProcessSyncNeverReopensTerminalFailureForRetryMetadata(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds-terminal-failed", "log-terminal-failed", nil, nil)
	h.syncLogRepo.setStatus(h.syncLogID, types.SyncLogStatusFailed)
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: h.ds.ID, TenantID: h.ds.TenantID, SyncLogID: h.syncLogID, ForceFull: true,
	})
	require.NoError(t, err)
	retryCtx := types.WithTaskRetryMetadata(context.Background(), 1, 5)
	require.NoError(t, h.svc.ProcessSync(retryCtx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	stored := h.syncLogRepo.snapshot(h.syncLogID)
	require.NotNil(t, stored)
	require.Equal(t, types.SyncLogStatusFailed, stored.Status)
	require.Empty(t, h.knowledgeSvc.deleted)
}

func TestProcessSyncStreamingLiteRetryUsesCheckpointCursor(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds-stream-retry", "log-stream-retry", nil, nil)
	h.ds.SyncMode = types.SyncModeFull
	cursorJSON, err := (&types.SyncCursor{ConnectorCursor: map[string]interface{}{"checkpoint": "page-7"}}).ToJSON()
	require.NoError(t, err)
	h.ds.LastSyncCursor = cursorJSON
	connector := &captureStreamingRetryConnector{}
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(connector))
	h.svc.connectorRegistry = registry
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: h.ds.ID, TenantID: h.ds.TenantID, SyncLogID: h.syncLogID, ForceFull: true,
	})
	require.NoError(t, err)
	ctx := types.WithTaskRetryMetadata(context.Background(), 1, 3)
	require.Error(t, h.svc.ProcessSync(ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	require.NotNil(t, connector.startCursor)
	require.Equal(t, "page-7", connector.startCursor.ConnectorCursor["checkpoint"])
}

func TestUpdateSyncRunResultFallbackPersistsDataSourceBeforeCASCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &types.SyncLog{ID: "post-cas-log", Status: types.SyncLogStatusRunning}
	storedLog := *log
	logs := &postCASCancelSyncLogRepo{
		// The repository owns a distinct stored row, just like the SQL
		// implementation. updateSyncRunResult is allowed to mutate its local
		// copy before issuing the running-CAS.
		processSyncSyncLogRepo: &processSyncSyncLogRepo{logs: map[string]*types.SyncLog{log.ID: &storedLog}},
		cancel:                 cancel,
	}
	dsRepo := &postCASStateDSRepo{recordingDSRepo: &recordingDSRepo{}}
	now := time.Now().UTC()
	ds := &types.DataSource{
		ID: "post-cas-ds", TenantID: 1, KnowledgeBaseID: "kb", Status: types.DataSourceStatusActive,
		LastSyncAt: &now, LastSyncCursor: types.JSON(`{"connector_cursor":{"page":"done"}}`),
	}
	svc := &DataSourceService{dsRepo: dsRepo, syncLogRepo: logs}
	result := &types.SyncResult{Created: 2}
	resultJSON, err := result.ToJSON()
	require.NoError(t, err)

	require.NoError(t, svc.updateSyncRunResult(ctx, ds, log, result, resultJSON,
		types.SyncLogStatusSuccess, "", false, false))
	require.False(t, dsRepo.sawCanceled, "post-CAS datasource persistence must not inherit watcher cancellation")
	require.Len(t, dsRepo.updated, 1)
	assert.Equal(t, types.SyncLogStatusSuccess, logs.snapshot(log.ID).Status)
	assert.Equal(t, resultJSON.ToString(), dsRepo.updated[0].LastSyncResult.ToString())
	require.NotNil(t, dsRepo.updated[0].LastSyncAt)
}

func TestUpdateSyncRunResultAtomicallyCommitsDataSourceOutcome(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "outcome.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.DataSource{}, &types.SyncLog{}))
	sources := repository.NewDataSourceRepository(db)
	logs := repository.NewSyncLogRepository(db)
	ds := &types.DataSource{
		ID: "atomic-outcome-ds", TenantID: 1, KnowledgeBaseID: "kb", Name: "Source",
		Type: types.ConnectorTypeGitHub, Status: types.DataSourceStatusError, ErrorMessage: "previous failure",
	}
	require.NoError(t, sources.Create(context.Background(), ds))
	log := &types.SyncLog{
		ID: "atomic-outcome-log", DataSourceID: ds.ID, TenantID: ds.TenantID, Status: types.SyncLogStatusRunning,
	}
	require.NoError(t, logs.Create(context.Background(), log))
	now := time.Now().UTC()
	ds.LastSyncAt = &now
	ds.LastSyncCursor = types.JSON(`{"connector_cursor":{"revision":"new"}}`)
	result := &types.SyncResult{Created: 2}
	resultJSON, err := result.ToJSON()
	require.NoError(t, err)
	svc := &DataSourceService{dsRepo: sources, syncLogRepo: logs}
	require.NoError(t, svc.updateSyncRunResult(context.Background(), ds, log, result, resultJSON,
		types.SyncLogStatusSuccess, "", false, false))

	storedDS, err := sources.FindByID(context.Background(), ds.ID)
	require.NoError(t, err)
	assert.Equal(t, types.DataSourceStatusActive, storedDS.Status)
	assert.Empty(t, storedDS.ErrorMessage)
	assert.Equal(t, resultJSON.ToString(), storedDS.LastSyncResult.ToString())
	require.NotNil(t, storedDS.LastSyncAt)
	storedLog, err := logs.FindByID(context.Background(), log.ID)
	require.NoError(t, err)
	assert.Equal(t, types.SyncLogStatusSuccess, storedLog.Status)
	assert.Equal(t, resultJSON.ToString(), storedLog.Result.ToString())
}

func TestProcessSyncFinalRetryDeadlinePersistsFailure(t *testing.T) {
	h := newSyncDeletionHarness(t, true, "ds-final-deadline", "log-final-deadline", nil, nil)
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(deadlineFetchConnector{}))
	h.svc.connectorRegistry = registry
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: h.ds.ID, TenantID: h.ds.TenantID, SyncLogID: h.syncLogID, ForceFull: true,
	})
	require.NoError(t, err)
	base := types.WithTaskRetryMetadata(context.Background(), 0, 0)
	ctx, cancel := context.WithTimeout(base, 30*time.Millisecond)
	defer cancel()
	err = h.svc.ProcessSync(ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	stored := h.syncLogRepo.snapshot(h.syncLogID)
	require.NotNil(t, stored)
	require.Equal(t, types.SyncLogStatusFailed, stored.Status)
	require.NotNil(t, stored.FinishedAt)
	require.Contains(t, stored.ErrorMessage, "Sync execution interrupted")
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
