package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

var errSyncAccessChanged = errors.New("source synchronization access changed")
var errSyncSourceRemoved = errors.New("data source was deleted")
var errSyncRunTerminated = errors.New("source synchronization run is no longer running")

// Revalidate at bounded batch boundaries and before committing a cursor. The
// check consumes current database grants; a fetched batch is not authorization
// to keep writing after the source or its folder grant has been withdrawn.
const syncAccessBatchSize = 16

// Sync runs are normally short enough that the access guard's bounded checks
// catch an operator pause or deletion. A sync log is a separate stop signal:
// deletion/startup cleanup can make it terminal while a connector is still
// fetching. Poll it at a small, bounded interval so connectors that honour a
// context stop promptly instead of continuing to ingest after the run has
// already been cancelled or failed.
const syncRunStateCheckTimeout = 5 * time.Second

// Once a compare-and-set result write has made a run terminal, its watcher may
// observe that terminal state and cancel the worker context immediately. The
// datasource state write which follows is part of the same outcome, so give it
// a short detached budget rather than letting that observation leave the log
// successful while LastSyncAt/cursor/result stay stale.
const syncRunPostResultStateTimeout = 5 * time.Second

func detachedSyncRunPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), syncRunPostResultStateTimeout)
}

var syncRunTerminalPollInterval = time.Second

type syncRunGuard struct {
	mu        sync.Mutex
	svc       *DataSourceService
	syncLogID string
	lastCheck time.Time
}

func (g *syncRunGuard) beforeItem(ctx context.Context, index int) error {
	if g == nil {
		return ctx.Err()
	}
	g.mu.Lock()
	lastCheck := g.lastCheck
	g.mu.Unlock()
	if index%syncAccessBatchSize == 0 || time.Since(lastCheck) >= time.Second {
		return g.check(ctx)
	}
	return ctx.Err()
}

// check deliberately probes with a detached, short-lived context. If the
// watcher has cancelled the worker context, we still need to distinguish a
// terminal sync log (which must not be retried or overwritten) from an
// ordinary task timeout.
func (g *syncRunGuard) check(ctx context.Context) error {
	if g == nil || g.syncLogID == "" {
		return ctx.Err()
	}
	ctxErr := ctx.Err()
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), syncRunStateCheckTimeout)
	defer cancel()
	log, err := g.svc.syncLogRepo.FindByID(probeCtx, g.syncLogID)
	if err != nil {
		if ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("cannot verify current sync run: %w", err)
	}
	if log == nil || log.Status != types.SyncLogStatusRunning {
		status := "missing"
		if log != nil {
			status = log.Status
		}
		return fmt.Errorf("%w: status=%s", errSyncRunTerminated, status)
	}
	if ctxErr != nil {
		return ctxErr
	}
	g.mu.Lock()
	g.lastCheck = time.Now()
	g.mu.Unlock()
	return nil
}

// watch cancels the supplied task context only after the durable sync log has
// reached a terminal state. Read failures are intentionally not treated as a
// stop signal: a transient database outage should remain retryable rather than
// turn a healthy sync into a silent cancellation.
func (g *syncRunGuard) watch(ctx context.Context, cancel context.CancelFunc) func() {
	if g == nil || g.syncLogID == "" {
		return func() {}
	}
	done := make(chan struct{})
	interval := syncRunTerminalPollInterval
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := g.check(ctx); errors.Is(err, errSyncRunTerminated) {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
	}
}

// stopSyncAfterRunTermination keeps the already-persisted terminal log intact.
// In particular, a startup/reset path may have marked it failed while an old
// Lite worker was still unwinding; that worker must neither overwrite the
// terminal state nor enqueue another retry.
func stopSyncAfterRunTermination(err error) error {
	return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
}

func ensureSyncRunActive(ctx context.Context, guard *syncRunGuard) error {
	if guard == nil {
		return ctx.Err()
	}
	if err := guard.check(ctx); err != nil {
		if errors.Is(err, errSyncRunTerminated) {
			return stopSyncAfterRunTermination(err)
		}
		return err
	}
	return nil
}

// syncTaskAttempt resolves retry metadata in both native Asynq and Lite mode.
// Lite stores the same counters under WeKnora's context key because Asynq's
// internal context values are not available to an in-process executor.
func syncTaskAttempt(ctx context.Context) (attempt, maxRetry int, ok bool) {
	if attempt, ok = asynq.GetRetryCount(ctx); ok {
		maxRetry, _ = asynq.GetMaxRetry(ctx)
		return attempt, maxRetry, true
	}
	if attempt, maxRetry, ok = types.TaskRetryMetadataFromContext(ctx); ok {
		return attempt, maxRetry, true
	}
	return 0, 0, false
}

func syncTaskCanRetry(ctx context.Context) bool {
	attempt, maxRetry, ok := syncTaskAttempt(ctx)
	return ok && attempt < maxRetry
}

// finishSyncRunGuardError persists a real execution failure even when the
// worker context is already cancelled or timed out. A terminal-log signal is
// the sole exception: it was written elsewhere and must remain untouched.
func (s *DataSourceService) finishSyncRunGuardError(
	ctx context.Context,
	ds *types.DataSource,
	log *types.SyncLog,
	result *types.SyncResult,
	wasPaused bool,
	err error,
) error {
	if err == nil || errors.Is(err, asynq.SkipRetry) {
		return err
	}
	return s.failSyncRun(ctx, ds, log, result,
		fmt.Sprintf("Sync execution interrupted: %v", err), wasPaused, err, true)
}

type syncAccessGuard struct {
	svc         *DataSourceService
	ds          *types.DataSource
	config      *types.DataSourceConfig
	allowPaused bool
	lastChecked time.Time
}

func (g *syncAccessGuard) beforeItem(ctx context.Context, index int) error {
	if index%syncAccessBatchSize == 0 || time.Since(g.lastChecked) >= time.Second {
		return g.check(ctx)
	}
	return ctx.Err()
}

func (g *syncAccessGuard) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := g.svc.dsRepo.FindByID(ctx, g.ds.ID)
	if err != nil {
		if err.Error() == "data source not found" {
			return fmt.Errorf("%w: %w", errSyncAccessChanged, errSyncSourceRemoved)
		}
		return fmt.Errorf("cannot verify current data source: %w", err)
	}
	if current == nil || current.TenantID != g.ds.TenantID || current.KnowledgeBaseID != g.ds.KnowledgeBaseID || current.Type != g.ds.Type {
		return fmt.Errorf("%w: data source identity changed", errSyncAccessChanged)
	}
	if current.Status == types.DataSourceStatusPaused && !g.allowPaused {
		return fmt.Errorf("%w: data source was paused", errSyncAccessChanged)
	}
	if g.allowPaused && current.Status != types.DataSourceStatusPaused {
		return fmt.Errorf("%w: paused data source was resumed; start a new sync", errSyncAccessChanged)
	}
	if current.Status != types.DataSourceStatusActive && current.Status != types.DataSourceStatusError && !(g.allowPaused && current.Status == types.DataSourceStatusPaused) {
		return fmt.Errorf("%w: data source is not active", errSyncAccessChanged)
	}
	cfg, err := current.ParseConfig()
	if err != nil || cfg == nil || g.config == nil || snapshot.Selection(cfg) != snapshot.Selection(g.config) || !slices.Equal(cfg.ResourceIDs, g.config.ResourceIDs) || current.SyncDeletions != g.ds.SyncDeletions {
		return fmt.Errorf("%w: source settings changed; start a new sync", errSyncAccessChanged)
	}
	if current.Type == localfolder.Type {
		if g.svc.connectorRegistry == nil {
			return errors.New("server folder registry unavailable")
		}
		connector, err := g.svc.connectorRegistry.Get(localfolder.Type)
		local, ok := connector.(*localfolder.Connector)
		if err != nil || !ok {
			return errors.New("server folder registry unavailable")
		}
		if _, err = local.AuthorizedRoot(ctx, current.TenantID, cfg); err != nil {
			if err.Error() == "server folder root is not authorized for this knowledge base" {
				return fmt.Errorf("%w: server folder authorization is no longer available", errSyncAccessChanged)
			}
			return fmt.Errorf("cannot verify current folder authorization: %w", err)
		}
	}
	g.lastChecked = time.Now()
	return nil
}

// Do not write stale datasource state when an operator has paused, removed or
// reconfigured it. Completed imports are retained; unprocessed items and the old
// cursor are left alone. A deliberate revocation must not be retried later.
func (s *DataSourceService) stopSyncAfterAccessChange(
	ctx context.Context,
	ds *types.DataSource,
	log *types.SyncLog,
	result *types.SyncResult,
	wasPaused bool,
	err error,
) error {
	// Context expiry and verification outages are retryable execution failures,
	// not an operator revocation. Keep their lease running until the final task
	// attempt, while a real access change remains an immediate cancellation.
	if !errors.Is(err, errSyncAccessChanged) {
		return s.finishSyncRunGuardError(ctx, ds, log, result, wasPaused, err)
	}
	if result == nil {
		result = &types.SyncResult{}
	}
	log.Status = types.SyncLogStatusCanceled
	log.ErrorMessage = err.Error()
	log.FinishedAt = timePtr(time.Now().UTC())
	log.ItemsTotal, log.ItemsCreated, log.ItemsUpdated = result.Total, result.Created, result.Updated
	log.ItemsDeleted, log.ItemsSkipped, log.ItemsFailed = result.Deleted, result.Skipped, result.Failed
	log.Result, _ = result.ToJSON()
	saveCtx, cancel := detachedSyncRunPersistenceContext(ctx)
	defer cancel()
	applied, saveErr := s.syncLogRepo.UpdateResultIfRunning(saveCtx, log)
	if saveErr != nil {
		return fmt.Errorf("record stopped sync: %w", saveErr)
	}
	if !applied {
		return stopSyncAfterRunTermination(errSyncRunTerminated)
	}
	return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
}
