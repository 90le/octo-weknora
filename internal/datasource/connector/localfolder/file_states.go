package localfolder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm/clause"
)

var localFileStatuses = map[string]bool{
	"indexed": true, "processing": true, "failed": true, "ignored": true,
	"missing_retained": true, "pending_delete": true, "removed": true,
}

// RecordFileState persists the observed and indexed versions of a source file.
func (c *Connector) RecordFileState(
	ctx context.Context,
	tenant uint64,
	source string,
	state types.LocalSourceFileState,
) error {
	if c.registry == nil || tenant == 0 || source == "" || len(state.Selection) != 64 || state.Path == "" ||
		!localFileStatuses[state.Status] {
		return errors.New("invalid local file observation")
	}
	sum := sha256.Sum256([]byte(state.Path))
	state.TenantID, state.DataSourceID, state.PathHash = tenant, source, hex.EncodeToString(sum[:])
	state.ObservedAt = time.Now().UTC()
	return c.registry.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"},
			{Name: "data_source_id"},
			{Name: "selection"},
			{Name: "path_hash"},
		},
		DoUpdates: clause.AssignmentColumns(
			[]string{
				"path",
				"status",
				"reason",
				"source_size",
				"source_version",
				"synced_version",
				"has_previous",
				"observed_at",
			},
		),
	}).Create(&state).Error
}

// KnownFileStates returns stored observations for a source selection.
func (c *Connector) KnownFileStates(
	ctx context.Context,
	tenant uint64,
	source, selection string,
) ([]types.LocalSourceFileState, error) {
	if c.registry == nil || tenant == 0 || source == "" || len(selection) != 64 {
		return nil, errors.New("invalid local file listing")
	}
	items := []types.LocalSourceFileState{}
	err := c.registry.db.WithContext(ctx).
		Where("tenant_id = ? AND data_source_id = ? AND selection = ?", tenant, source, selection).
		Find(&items).
		Error
	return items, err
}

// ListFileStates returns filtered observations and status counts.
func (c *Connector) ListFileStates(
	ctx context.Context,
	tenant uint64,
	source, selection, status string,
	offset int,
) (*types.LocalSourceFilePage, error) {
	if c.registry == nil || tenant == 0 || source == "" || len(selection) != 64 ||
		(status != "" && !localFileStatuses[status]) ||
		offset < 0 {
		return nil, errors.New("invalid local file listing")
	}
	base := c.registry.db.WithContext(ctx).
		Model(&types.LocalSourceFileState{}).
		Where("tenant_id = ? AND data_source_id = ? AND selection = ?", tenant, source, selection)
	var groups []struct {
		Status string
		Count  int64
	}
	if err := base.Select("status, COUNT(*) AS count").Group("status").Scan(&groups).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, group := range groups {
		counts[group.Status] = group.Count
	}
	q := c.registry.db.WithContext(ctx).
		Where("tenant_id = ? AND data_source_id = ? AND selection = ?", tenant, source, selection)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	items := []types.LocalSourceFileState{}
	if err := q.Order("path ASC").Limit(101).Offset(offset).Find(&items).Error; err != nil {
		return nil, err
	}
	more := len(items) > 100
	if more {
		items = items[:100]
	}
	// File ingestion can succeed while individual image OCR output is rejected.
	// Surface that partial result without changing the indexed document or its
	// sync cursor. Include only the latest attempt for each image in the current
	// canonical document, and scope the join to the source's tenant and KB.
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.Status == "indexed" || item.HasPrevious {
			paths = append(paths, item.Path)
		}
	}
	if len(paths) != 0 {
		var warnings []struct {
			Path  string
			Count int64
		}
		const query = `
WITH latest AS (
  SELECT k.metadata->>'source_path' AS path, s.status,
         ROW_NUMBER() OVER (
           PARTITION BY k.id, s.name ORDER BY s.attempt DESC, s.updated_at DESC, s.id DESC
         ) AS rank
  FROM knowledges AS k
  JOIN data_sources AS d ON d.id = ? AND d.tenant_id = ?
    AND d.deleted_at IS NULL AND d.knowledge_base_id = k.knowledge_base_id AND k.tenant_id = d.tenant_id
  JOIN local_source_file_states AS f ON f.tenant_id = d.tenant_id AND f.data_source_id = d.id
    AND f.selection = ? AND f.path = k.metadata->>'source_path'
    AND f.synced_version <> '' AND f.synced_version = k.metadata->>'source_version'
  JOIN knowledge_processing_spans AS s ON s.knowledge_id = k.id
  WHERE k.deleted_at IS NULL AND k.metadata->>'datasource_id' = d.id
    AND k.metadata->>'sync_target_external_id' IS NULL
    AND k.metadata->>'source_resource_id' = d.config->'settings'->>'root_id'
    AND k.metadata->>'source_path' IN ? AND s.name LIKE 'multimodal.image[%.ocr'
)
SELECT path, COUNT(*) AS count FROM latest WHERE rank = 1 AND status = 'failed' GROUP BY path`
		if err := c.registry.db.WithContext(ctx).
			Raw(query, source, tenant, selection, paths).
			Scan(&warnings).Error; err != nil {
			return nil, err
		}
		countsByPath := make(map[string]int64, len(warnings))
		for _, warning := range warnings {
			countsByPath[warning.Path] = warning.Count
		}
		for i := range items {
			items[i].OCRFailedImages = countsByPath[items[i].Path]
		}
	}
	return &types.LocalSourceFilePage{Items: items, Counts: counts, Offset: offset, HasMore: more}, nil
}
