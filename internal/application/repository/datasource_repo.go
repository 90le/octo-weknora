package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// DataSourceRepository provides data access for data sources
type DataSourceRepository struct {
	db *gorm.DB
}

// NewDataSourceRepository creates a new data source repository
func NewDataSourceRepository(db *gorm.DB) interfaces.DataSourceRepository {
	return &DataSourceRepository{db: db}
}

// Create inserts a new data source record
func (r *DataSourceRepository) Create(ctx context.Context, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	// GORM treats false as the zero value of bool. For a field tagged
	// default:true it replaces both the INSERT value and the in-memory field
	// with true, so a caller-selected false would be lost. Capture it, force
	// the column write, then restore the struct so Create's return value (and
	// the HTTP 201 body) match the database.
	syncDeletions := ds.SyncDeletions
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ds).Error; err != nil {
			return err
		}
		return tx.Model(&types.DataSource{}).
			Where("id = ?", ds.ID).
			UpdateColumn("sync_deletions", syncDeletions).Error
	})
	ds.SyncDeletions = syncDeletions
	return err
}

// FindByID retrieves a data source by ID
func (r *DataSourceRepository) FindByID(ctx context.Context, id string) (*types.DataSource, error) {
	if id == "" {
		return nil, errors.New("id is empty")
	}
	var ds types.DataSource
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Where("deleted_at IS NULL").
		First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("data source not found")
		}
		return nil, err
	}
	return &ds, nil
}

// FindByKnowledgeBase lists all data sources for a knowledge base
func (r *DataSourceRepository) FindByKnowledgeBase(ctx context.Context, kbID string) ([]*types.DataSource, error) {
	if kbID == "" {
		return nil, errors.New("knowledge base id is empty")
	}
	var dataSources []*types.DataSource
	if err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Find(&dataSources).Error; err != nil {
		return nil, err
	}
	return dataSources, nil
}

// Update updates an existing data source
func (r *DataSourceRepository) Update(ctx context.Context, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	if ds.ID == "" {
		return errors.New("data source id is empty")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(ds).Updates(ds).Error; err != nil {
			return err
		}
		// GORM Updates(struct) deliberately skips zero values, which would make
		// a user-selected sync_deletions=false impossible to persist.
		return tx.Model(&types.DataSource{}).
			Where("id = ?", ds.ID).
			UpdateColumn("sync_deletions", ds.SyncDeletions).Error
	})
}

// UpdateGitHubScheduleIfUnchanged is the narrow compare-and-swap used by an
// administrator-confirmed migration of the old GitHub batch default. It never
// changes source configuration, status or sync state. The active sync check is
// part of the same SQL statement as the update, not an earlier advisory read.
func (r *DataSourceRepository) UpdateGitHubScheduleIfUnchanged(
	ctx context.Context, tenantID uint64, kbID, dsID string, expectedUpdatedAt time.Time, proposed string,
) (bool, error) {
	result := r.db.WithContext(ctx).Model(&types.DataSource{}).
		Where("data_sources.id = ? AND data_sources.tenant_id = ? AND data_sources.knowledge_base_id = ?", dsID, tenantID, kbID).
		Where("data_sources.type = ? AND data_sources.status = ? AND data_sources.sync_schedule = ? AND data_sources.updated_at = ?",
			types.ConnectorTypeGitHub, types.DataSourceStatusActive, "0 0 */6 * * *", expectedUpdatedAt).
		Where("NOT EXISTS (SELECT 1 FROM sync_logs WHERE sync_logs.data_source_id = data_sources.id AND sync_logs.status = ?)",
			types.SyncLogStatusRunning).
		Updates(map[string]interface{}{"sync_schedule": proposed, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// UpdateSyncState updates only fields managed by sync execution. GORM's
// Updates(struct) skips zero values, so use a map here to persist cleared error
// messages without broadening the generic Update method.
func (r *DataSourceRepository) UpdateSyncState(ctx context.Context, ds *types.DataSource) error {
	return updateDataSourceSyncState(r.db.WithContext(ctx), ds)
}

func updateDataSourceSyncState(db *gorm.DB, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	if ds.ID == "" {
		return errors.New("data source id is empty")
	}
	result := db.
		Model(&types.DataSource{}).
		Where("id = ?", ds.ID).
		Where("deleted_at IS NULL").
		Updates(map[string]interface{}{
			"status":           ds.Status,
			"last_sync_at":     ds.LastSyncAt,
			"last_sync_cursor": ds.LastSyncCursor,
			"last_sync_result": ds.LastSyncResult,
			"error_message":    ds.ErrorMessage,
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("data source not found")
	}
	return nil
}

// Delete performs a soft delete
func (r *DataSourceRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id is empty")
	}
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&types.DataSource{}).Error; err != nil {
		return err
	}
	return nil
}

// FindActive retrieves all active data sources (used for scheduling)
func (r *DataSourceRepository) FindActive(ctx context.Context) ([]*types.DataSource, error) {
	var dataSources []*types.DataSource
	if err := r.db.WithContext(ctx).
		Where("status = ?", types.DataSourceStatusActive).
		Where("deleted_at IS NULL").
		Where("sync_schedule != ''").
		Order("created_at DESC").
		Find(&dataSources).Error; err != nil {
		return nil, err
	}
	return dataSources, nil
}

// SyncLogRepository provides data access for sync logs
type SyncLogRepository struct {
	db *gorm.DB
}

var errSyncLogNoLongerRunning = errors.New("sync log is no longer running")

// NewSyncLogRepository creates a new sync log repository
func NewSyncLogRepository(db *gorm.DB) interfaces.SyncLogRepository {
	return &SyncLogRepository{db: db}
}

// Create inserts a new sync log entry
func (r *SyncLogRepository) Create(ctx context.Context, log *types.SyncLog) error {
	if log == nil {
		return errors.New("sync log is nil")
	}
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return err
	}
	return nil
}

// FindByID retrieves a sync log by ID
func (r *SyncLogRepository) FindByID(ctx context.Context, id string) (*types.SyncLog, error) {
	if id == "" {
		return nil, errors.New("id is empty")
	}
	var log types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("sync log not found")
		}
		return nil, err
	}
	return &log, nil
}

// FindByDataSource lists sync logs for a data source with pagination
func (r *SyncLogRepository) FindByDataSource(ctx context.Context, dsID string, limit int, offset int) ([]*types.SyncLog, error) {
	if dsID == "" {
		return nil, errors.New("data source id is empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	var logs []*types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("data_source_id = ?", dsID).
		Order("started_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// FindLatest retrieves the most recent sync log for a data source
func (r *SyncLogRepository) FindLatest(ctx context.Context, dsID string) (*types.SyncLog, error) {
	if dsID == "" {
		return nil, errors.New("data source id is empty")
	}
	var log types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("data_source_id = ?", dsID).
		Order("started_at DESC").
		Limit(1).
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &log, nil
}

// HasRunningSync checks if a data source has any sync currently in "running" status.
func (r *SyncLogRepository) HasRunningSync(ctx context.Context, dsID string) (bool, error) {
	if dsID == "" {
		return false, errors.New("data source id is empty")
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&types.SyncLog{}).
		Where("data_source_id = ?", dsID).
		Where("status = ?", types.SyncLogStatusRunning).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Update updates an existing sync log entry
func (r *SyncLogRepository) Update(ctx context.Context, log *types.SyncLog) error {
	if log == nil {
		return errors.New("sync log is nil")
	}
	if log.ID == "" {
		return errors.New("sync log id is empty")
	}
	if err := r.db.WithContext(ctx).
		Model(log).
		Updates(log).Error; err != nil {
		return err
	}
	return nil
}

// UpdateResult updates only fields produced by sync execution. Use an explicit
// map so empty error messages are written when a later sync succeeds.
func (r *SyncLogRepository) UpdateResult(ctx context.Context, log *types.SyncLog) error {
	_, err := r.updateResult(ctx, log, false)
	return err
}

// UpdateResultIfRunning is the compare-and-set write used by a live sync
// worker. A cancellation or restart-recovery failure is terminal; a stale
// worker must observe that transition rather than replacing it with a late
// checkpoint or success result.
func (r *SyncLogRepository) UpdateResultIfRunning(ctx context.Context, log *types.SyncLog) (bool, error) {
	return r.updateResult(ctx, log, true)
}

// UpdateResultAndDataSourceIfRunning commits the sync outcome and the paired
// datasource cursor/status in one transaction. The datasource row is updated
// first, matching the datasource-delete → sync-log-cancel lock order. If the
// log CAS subsequently misses, the transaction rolls that datasource update
// back; a terminal cancellation can never coexist with this run's stale
// cursor/result.
func (r *SyncLogRepository) UpdateResultAndDataSourceIfRunning(
	ctx context.Context,
	log *types.SyncLog,
	ds *types.DataSource,
) (bool, error) {
	if ds == nil {
		return false, errors.New("data source is nil")
	}
	if ds.ID == "" {
		return false, errors.New("data source id is empty")
	}
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := updateDataSourceSyncState(tx, ds); err != nil {
			return err
		}
		written, err := r.updateResultWithDB(tx, log, true)
		if err != nil {
			return err
		}
		if !written {
			return errSyncLogNoLongerRunning
		}
		applied = true
		return nil
	})
	if errors.Is(err, errSyncLogNoLongerRunning) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (r *SyncLogRepository) updateResult(ctx context.Context, log *types.SyncLog, onlyRunning bool) (bool, error) {
	return r.updateResultWithDB(r.db.WithContext(ctx), log, onlyRunning)
}

func (r *SyncLogRepository) updateResultWithDB(db *gorm.DB, log *types.SyncLog, onlyRunning bool) (bool, error) {
	if log == nil {
		return false, errors.New("sync log is nil")
	}
	if log.ID == "" {
		return false, errors.New("sync log id is empty")
	}
	query := db.
		Model(&types.SyncLog{}).
		Where("id = ?", log.ID)
	if onlyRunning {
		query = query.Where("status = ?", types.SyncLogStatusRunning)
	}
	result := query.Updates(map[string]interface{}{
		"status":        log.Status,
		"finished_at":   log.FinishedAt,
		"items_total":   log.ItemsTotal,
		"items_created": log.ItemsCreated,
		"items_updated": log.ItemsUpdated,
		"items_deleted": log.ItemsDeleted,
		"items_skipped": log.ItemsSkipped,
		"items_failed":  log.ItemsFailed,
		"error_message": log.ErrorMessage,
		"result":        log.Result,
		"updated_at":    time.Now().UTC(),
	})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// CancelPendingByDataSource marks all non-terminal sync logs for a data source as canceled.
func (r *SyncLogRepository) CancelPendingByDataSource(ctx context.Context, dsID string) error {
	if dsID == "" {
		return errors.New("data source id is empty")
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).
		Model(&types.SyncLog{}).
		Where("data_source_id = ?", dsID).
		Where("status IN ?", []string{types.SyncLogStatusRunning, "pending"}).
		Updates(map[string]interface{}{
			"status":        types.SyncLogStatusCanceled,
			"finished_at":   &now,
			"error_message": "data source deleted",
		}).Error
}

// CleanupOldLogs deletes sync logs older than the retention period
func (r *SyncLogRepository) CleanupOldLogs(ctx context.Context, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	// Delete logs older than the retention period
	if err := r.db.WithContext(ctx).
		Where("started_at < NOW() - INTERVAL ? DAY", retentionDays).
		Delete(&types.SyncLog{}).Error; err != nil {
		return err
	}
	return nil
}
