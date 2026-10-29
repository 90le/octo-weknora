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
	return &types.LocalSourceFilePage{Items: items, Counts: counts, Offset: offset, HasMore: more}, nil
}
