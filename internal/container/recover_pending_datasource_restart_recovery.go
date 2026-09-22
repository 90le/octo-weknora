package container

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

// recoverPendingDataSourceRestartRecoveryRuns restores the ephemeral task
// trigger for an already-approved durable recovery plan. It does not discover
// candidates, acquire leases, fetch GitHub, or mutate a knowledge record; the
// worker repeats its normal digest/ownership guards before doing any work.
func recoverPendingDataSourceRestartRecoveryRuns(db *gorm.DB, task interfaces.TaskEnqueuer) {
	if db == nil || task == nil {
		return
	}
	ctx := context.Background()
	activeSource := `EXISTS (
		SELECT 1 FROM data_sources ds
		JOIN knowledge_bases kb ON kb.id = ds.knowledge_base_id
		WHERE ds.id = datasource_restart_recovery_runs.data_source_id
		  AND ds.tenant_id = datasource_restart_recovery_runs.tenant_id
		  AND ds.knowledge_base_id = datasource_restart_recovery_runs.knowledge_base_id
		  AND ds.deleted_at IS NULL AND kb.deleted_at IS NULL
	)`
	// A deleted/rebound source must never be revived by a stale task trigger.
	now := time.Now().UTC()
	invalid := db.WithContext(ctx).Model(&types.DataSourceRestartRecoveryRun{}).
		Where("status IN ?", []string{types.DataSourceRestartRecoveryStatusPending, types.DataSourceRestartRecoveryStatusRunning}).
		Where("NOT " + activeSource).
		Updates(map[string]interface{}{
			"status":        types.DataSourceRestartRecoveryStatusBlocked,
			"error_message": "data source or knowledge base was removed before recovery could resume",
			"finished_at":   &now,
			"updated_at":    now,
		})
	if invalid.Error != nil {
		logger.Warnf(ctx, "[RestartRecovery] failed to block stale recovery runs: %v", invalid.Error)
		return
	}
	var runs []*types.DataSourceRestartRecoveryRun
	if err := db.WithContext(ctx).
		Where("status IN ?", []string{types.DataSourceRestartRecoveryStatusPending, types.DataSourceRestartRecoveryStatusRunning}).
		Where(activeSource).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		logger.Warnf(ctx, "[RestartRecovery] failed to list durable recovery runs: %v", err)
		return
	}
	rearmed := 0
	for _, run := range runs {
		if run == nil || run.ID == "" || run.TenantID == 0 || run.DataSourceID == "" {
			continue
		}
		payload, err := json.Marshal(types.DataSourceRestartRecoveryPayload{
			TenantID: run.TenantID, DataSourceID: run.DataSourceID, RunID: run.ID,
		})
		if err != nil {
			logger.Warnf(ctx, "[RestartRecovery] marshal run %s: %v", run.ID, err)
			continue
		}
		queued := asynq.NewTask(types.TypeDataSourceRestartRecovery, payload,
			asynq.Queue(types.QueueMaintenance), asynq.MaxRetry(3), asynq.Timeout(4*time.Hour),
			asynq.TaskID("datasource-restart-recovery-"+run.ID))
		if _, err := task.Enqueue(queued); err != nil {
			if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
				rearmed++
				continue
			}
			logger.Warnf(ctx, "[RestartRecovery] enqueue run %s: %v", run.ID, err)
			continue
		}
		rearmed++
	}
	if rearmed > 0 {
		logger.Infof(ctx, "[RestartRecovery] re-armed %d durable recovery run(s)", rearmed)
	}
}
