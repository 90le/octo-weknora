package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource/connector/localfolder"
	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

var errSyncAccessChanged = errors.New("source synchronization access changed")
var errSyncSourceRemoved = errors.New("data source was deleted")

// Revalidate at bounded batch boundaries and before committing a cursor. The
// check consumes current database grants; a fetched batch is not authorization
// to keep writing after the source or its folder grant has been withdrawn.
const syncAccessBatchSize = 16

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
func (s *DataSourceService) stopSyncAfterAccessChange(ctx context.Context, log *types.SyncLog, result *types.SyncResult, err error) error {
	if result == nil {
		result = &types.SyncResult{}
	}
	log.Status = types.SyncLogStatusFailed
	if errors.Is(err, errSyncAccessChanged) {
		log.Status = types.SyncLogStatusCanceled
	}
	log.ErrorMessage = err.Error()
	log.FinishedAt = timePtr(time.Now().UTC())
	log.ItemsTotal, log.ItemsCreated, log.ItemsUpdated = result.Total, result.Created, result.Updated
	log.ItemsDeleted, log.ItemsSkipped, log.ItemsFailed = result.Deleted, result.Skipped, result.Failed
	log.Result, _ = result.ToJSON()
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := s.syncLogRepo.UpdateResult(saveCtx, log); saveErr != nil {
		return fmt.Errorf("record stopped sync: %w", saveErr)
	}
	if errors.Is(err, errSyncAccessChanged) {
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	return err
}
