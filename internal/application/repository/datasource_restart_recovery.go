package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// TryAcquireRestartRecoveryLease is a database compare-and-set. A normal sync
// observes this lease before it can fetch remote content; recovery never
// relies on a process-local mutex for that boundary.
func (r *DataSourceRepository) TryAcquireRestartRecoveryLease(
	ctx context.Context,
	dataSourceID, leaseID string,
	leaseUntil time.Time,
) (bool, error) {
	if dataSourceID == "" || leaseID == "" || leaseUntil.IsZero() {
		return false, errors.New("restart recovery lease requires source, lease id, and expiry")
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).
		Model(&types.DataSource{}).
		Where("id = ? AND deleted_at IS NULL", dataSourceID).
		Where("restart_recovery_lease_until IS NULL OR restart_recovery_lease_until < ?", now).
		Updates(map[string]interface{}{
			"restart_recovery_lease_id":    leaseID,
			"restart_recovery_lease_until": leaseUntil.UTC(),
			"updated_at":                   now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// RenewRestartRecoveryLease extends only the currently-owned, still-live
// lease. A worker that lost ownership must stop rather than silently reviving
// a stale lease after another process recovered the source.
func (r *DataSourceRepository) RenewRestartRecoveryLease(
	ctx context.Context,
	dataSourceID, leaseID string,
	leaseUntil time.Time,
) (bool, error) {
	if dataSourceID == "" || leaseID == "" || leaseUntil.IsZero() {
		return false, errors.New("restart recovery lease requires source, lease id, and expiry")
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).
		Model(&types.DataSource{}).
		Where("id = ? AND deleted_at IS NULL", dataSourceID).
		Where("restart_recovery_lease_id = ?", leaseID).
		Where("restart_recovery_lease_until > ?", now).
		Updates(map[string]interface{}{
			"restart_recovery_lease_until": leaseUntil.UTC(),
			"updated_at":                   now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *DataSourceRepository) ReleaseRestartRecoveryLease(
	ctx context.Context,
	dataSourceID, leaseID string,
) error {
	if dataSourceID == "" || leaseID == "" {
		return errors.New("restart recovery lease requires source and lease id")
	}
	return r.db.WithContext(ctx).
		Model(&types.DataSource{}).
		Where("id = ? AND restart_recovery_lease_id = ?", dataSourceID, leaseID).
		Updates(map[string]interface{}{
			"restart_recovery_lease_id":    "",
			"restart_recovery_lease_until": nil,
			"updated_at":                   time.Now().UTC(),
		}).Error
}

func (r *DataSourceRepository) CreateRestartRecoveryRun(
	ctx context.Context,
	run *types.DataSourceRestartRecoveryRun,
) error {
	if run == nil || run.ID == "" || run.DataSourceID == "" || run.KnowledgeBaseID == "" || run.TenantID == 0 {
		return errors.New("restart recovery run is incomplete")
	}
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *DataSourceRepository) FindRestartRecoveryRun(
	ctx context.Context,
	runID string,
) (*types.DataSourceRestartRecoveryRun, error) {
	if runID == "" {
		return nil, errors.New("restart recovery run id is empty")
	}
	var run types.DataSourceRestartRecoveryRun
	if err := r.db.WithContext(ctx).Where("id = ?", runID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("restart recovery run not found")
		}
		return nil, err
	}
	return &run, nil
}

// ClaimRestartRecoveryRun pairs a durable run record with the data-source
// lease that was already acquired. It is intentionally a CAS, so duplicate
// queued triggers cannot both mutate the same recovery plan.
func (r *DataSourceRepository) ClaimRestartRecoveryRun(
	ctx context.Context,
	runID, leaseID string,
) (bool, error) {
	if runID == "" || leaseID == "" {
		return false, errors.New("restart recovery run id and lease id are required")
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).
		Model(&types.DataSourceRestartRecoveryRun{}).
		Where("id = ? AND status IN ?", runID,
			[]string{types.DataSourceRestartRecoveryStatusPending, types.DataSourceRestartRecoveryStatusRunning}).
		Where("lease_id = '' OR lease_id = ?", leaseID).
		Updates(map[string]interface{}{
			"status":     types.DataSourceRestartRecoveryStatusRunning,
			"lease_id":   leaseID,
			"started_at": gorm.Expr("COALESCE(started_at, ?)", now),
			"updated_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// BlockRestartRecoveryRunBeforeLease is used only when the source/KB vanished
// before a worker could acquire its source lease. It cannot mutate a completed
// run, and it deliberately clears no datasource state because no lease was
// ever owned.
func (r *DataSourceRepository) BlockRestartRecoveryRunBeforeLease(
	ctx context.Context,
	runID, message string,
) (bool, error) {
	if runID == "" {
		return false, errors.New("restart recovery run id is required")
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).
		Model(&types.DataSourceRestartRecoveryRun{}).
		Where("id = ? AND status IN ?", runID,
			[]string{types.DataSourceRestartRecoveryStatusPending, types.DataSourceRestartRecoveryStatusRunning}).
		Updates(map[string]interface{}{
			"status":        types.DataSourceRestartRecoveryStatusBlocked,
			"error_message": message,
			"finished_at":   &now,
			"updated_at":    now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// UpdateRestartRecoveryRunIfLease persists progress or a terminal state only
// for the worker which still owns the run. The plan JSON is the audit record:
// it includes candidate state but never raw source paths or credentials.
func (r *DataSourceRepository) UpdateRestartRecoveryRunIfLease(
	ctx context.Context,
	run *types.DataSourceRestartRecoveryRun,
	leaseID string,
) (bool, error) {
	if run == nil || run.ID == "" || leaseID == "" {
		return false, errors.New("restart recovery run and lease id are required")
	}
	values := map[string]interface{}{
		"plan":          run.Plan,
		"status":        run.Status,
		"error_message": run.ErrorMessage,
		"finished_at":   run.FinishedAt,
		"updated_at":    time.Now().UTC(),
	}
	result := r.db.WithContext(ctx).
		Model(&types.DataSourceRestartRecoveryRun{}).
		Where("id = ? AND lease_id = ? AND status = ?", run.ID, leaseID, types.DataSourceRestartRecoveryStatusRunning).
		Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *DataSourceRepository) ListRestartRecoveryRunsToResume(
	ctx context.Context,
) ([]*types.DataSourceRestartRecoveryRun, error) {
	var runs []*types.DataSourceRestartRecoveryRun
	if err := r.db.WithContext(ctx).
		Where("status IN ?", []string{
			types.DataSourceRestartRecoveryStatusPending,
			types.DataSourceRestartRecoveryStatusRunning,
		}).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}
