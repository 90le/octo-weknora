package repository

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrGitHubDocumentRunChanged = errors.New("GitHub document sync run changed or is no longer active")

func (r *DataSourceRepository) FindRunningGitHubDocumentRun(
	ctx context.Context, tenantID uint64, kbID, dsID string,
) (*types.GitHubDocumentRun, error) {
	var run types.GitHubDocumentRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ?", tenantID, kbID, dsID, types.GitHubDocumentRunRunning).
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *DataSourceRepository) CreateGitHubDocumentRun(
	ctx context.Context, run *types.GitHubDocumentRun, items []types.GitHubDocumentSyncItem,
) error {
	if run == nil || run.ID == "" || run.DataSourceID == "" || run.KnowledgeBaseID == "" || run.TenantID == 0 ||
		run.Selection == "" || run.CommitSHA == "" || run.PlanDigest == "" || run.CredentialScope == "" || len(run.TargetCursor) == 0 {
		return errors.New("GitHub document plan is incomplete")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&types.DataSource{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND type = ? AND deleted_at IS NULL",
				run.DataSourceID, run.TenantID, run.KnowledgeBaseID, types.ConnectorTypeGitHub).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrGitHubDocumentRunChanged
		}
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		for i := range items {
			if items[i].RunID != run.ID || items[i].PathHash == "" || items[i].Path == "" ||
				(items[i].Operation != types.GitHubDocumentItemUpsert && items[i].Operation != types.GitHubDocumentItemDelete) {
				return errors.New("GitHub document item is invalid")
			}
			if err := tx.Create(&items[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *DataSourceRepository) ListGitHubDocumentRunItems(
	ctx context.Context, run *types.GitHubDocumentRun,
) ([]types.GitHubDocumentSyncItem, error) {
	if run == nil || run.ID == "" {
		return nil, ErrGitHubDocumentRunChanged
	}
	var items []types.GitHubDocumentSyncItem
	err := r.db.WithContext(ctx).Model(&types.GitHubDocumentSyncItem{}).
		Joins("JOIN github_document_sync_runs AS runs ON runs.id = github_document_sync_items.run_id").
		Where("runs.id = ? AND runs.tenant_id = ? AND runs.knowledge_base_id = ? AND runs.data_source_id = ? AND runs.status = ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Operation != items[j].Operation {
			return items[i].Operation == types.GitHubDocumentItemUpsert
		}
		return items[i].Path < items[j].Path
	})
	return items, nil
}

func (r *DataSourceRepository) UpdateGitHubDocumentItem(
	ctx context.Context, run *types.GitHubDocumentRun, leaseID string, item types.GitHubDocumentSyncItem, status, outcome, errorCode string,
) error {
	if run == nil || leaseID == "" || item.RunID != run.ID || item.PathHash == "" || item.Path == "" ||
		(status != types.GitHubDocumentItemReady && status != types.GitHubDocumentItemFailed) {
		return ErrGitHubDocumentRunChanged
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.GitHubDocumentRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ? AND lease_id = ? AND lease_until > ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning, leaseID, time.Now().UTC()).
			First(&current).Error; err != nil {
			return ErrGitHubDocumentRunChanged
		}
		result := tx.Model(&types.GitHubDocumentSyncItem{}).
			Where("run_id = ? AND path_hash = ? AND path = ? AND blob_sha = ? AND operation = ?", item.RunID, item.PathHash, item.Path, item.BlobSHA, item.Operation).
			Updates(map[string]interface{}{
				"status": status, "outcome": outcome, "error_code": errorCode, "attempts": gorm.Expr("attempts + 1"), "updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrGitHubDocumentRunChanged
		}
		return nil
	})
}

func (r *DataSourceRepository) SupersedeGitHubDocumentRun(ctx context.Context, run *types.GitHubDocumentRun) error {
	if run == nil || run.ID == "" {
		return ErrGitHubDocumentRunChanged
	}
	result := r.db.WithContext(ctx).Model(&types.GitHubDocumentRun{}).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning).
		Updates(map[string]interface{}{"status": types.GitHubDocumentRunSuperseded, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrGitHubDocumentRunChanged
	}
	return nil
}

func (r *DataSourceRepository) AcquireGitHubDocumentRunLease(
	ctx context.Context, run *types.GitHubDocumentRun, leaseID string, until time.Time,
) (bool, error) {
	if run == nil || run.ID == "" || leaseID == "" || until.IsZero() {
		return false, ErrGitHubDocumentRunChanged
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.GitHubDocumentRun{}).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning).
		Where("lease_until IS NULL OR lease_until <= ? OR lease_id = ?", now, leaseID).
		Updates(map[string]interface{}{"lease_id": leaseID, "lease_until": until.UTC(), "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (r *DataSourceRepository) RenewGitHubDocumentRunLease(
	ctx context.Context, run *types.GitHubDocumentRun, leaseID string, until time.Time,
) (bool, error) {
	if run == nil || run.ID == "" || leaseID == "" || until.IsZero() {
		return false, ErrGitHubDocumentRunChanged
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.GitHubDocumentRun{}).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ? AND lease_id = ? AND lease_until > ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning, leaseID, now).
		Updates(map[string]interface{}{"lease_until": until.UTC(), "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (r *DataSourceRepository) ReleaseGitHubDocumentRunLease(ctx context.Context, run *types.GitHubDocumentRun, leaseID string) error {
	if run == nil || run.ID == "" || leaseID == "" {
		return ErrGitHubDocumentRunChanged
	}
	return r.db.WithContext(ctx).Model(&types.GitHubDocumentRun{}).
		Where("id = ? AND lease_id = ?", run.ID, leaseID).
		Updates(map[string]interface{}{"lease_id": "", "lease_until": nil, "updated_at": time.Now().UTC()}).Error
}

// PublishGitHubDocumentRun updates only the last complete cursor after every
// planned item is ready. Config equality fences a concurrent credential or
// selection change between the service's access check and this transaction.
func (r *DataSourceRepository) PublishGitHubDocumentRun(
	ctx context.Context, run *types.GitHubDocumentRun, leaseID string, ds *types.DataSource, syncLogID string,
) error {
	if run == nil || leaseID == "" || syncLogID == "" || ds == nil || run.TenantID != ds.TenantID || run.KnowledgeBaseID != ds.KnowledgeBaseID || run.DataSourceID != ds.ID {
		return ErrGitHubDocumentRunChanged
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owned types.GitHubDocumentRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ? AND lease_id = ? AND lease_until > ?",
			run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning, leaseID, time.Now().UTC(),
		).First(&owned).Error; err != nil {
			return ErrGitHubDocumentRunChanged
		}
		var pending int64
		if err := tx.Model(&types.GitHubDocumentSyncItem{}).
			Where("run_id = ? AND status <> ?", run.ID, types.GitHubDocumentItemReady).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return ErrGitHubDocumentRunChanged
		}
		now := time.Now().UTC()
		query := tx.Model(&types.DataSource{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND type = ? AND deleted_at IS NULL AND status = ? AND sync_deletions = ?",
				ds.ID, ds.TenantID, ds.KnowledgeBaseID, types.ConnectorTypeGitHub, ds.Status, ds.SyncDeletions)
		if tx.Dialector.Name() == "postgres" {
			query = query.Where("config = ?::jsonb", ds.Config.ToString())
		} else {
			// types.JSON is stored as a BLOB by SQLite's driver. Comparing it
			// with a Go string (TEXT) would miss despite identical bytes.
			query = query.Where("config = ?", ds.Config)
		}
		updated := query.Updates(map[string]interface{}{"last_sync_cursor": run.TargetCursor, "last_sync_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrGitHubDocumentRunChanged
		}
		// A cancellation that races with the final access check must roll back
		// this datasource cursor update. Lock in the same datasource -> sync-log
		// order used by the normal atomic sync outcome finalizer.
		var liveLog types.SyncLog
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND data_source_id = ? AND tenant_id = ? AND status = ?",
			syncLogID, ds.ID, ds.TenantID, types.SyncLogStatusRunning,
		).First(&liveLog).Error; err != nil {
			return ErrGitHubDocumentRunChanged
		}
		result := tx.Model(&types.GitHubDocumentRun{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND data_source_id = ? AND status = ? AND lease_id = ? AND lease_until > ?",
				run.ID, run.TenantID, run.KnowledgeBaseID, run.DataSourceID, types.GitHubDocumentRunRunning, leaseID, now).
			Updates(map[string]interface{}{"status": types.GitHubDocumentRunComplete, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrGitHubDocumentRunChanged
		}
		// Completed progress is derivable from the newly published cursor and
		// SyncLog result. Keep durable rows only while a run is incomplete.
		if err := tx.Where("run_id = ?", run.ID).Delete(&types.GitHubDocumentSyncItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ? AND status = ?", run.ID, types.GitHubDocumentRunComplete).
			Delete(&types.GitHubDocumentRun{}).Error; err != nil {
			return err
		}
		ds.LastSyncCursor = append(types.JSON(nil), run.TargetCursor...)
		ds.LastSyncAt = &now
		return nil
	})
}
