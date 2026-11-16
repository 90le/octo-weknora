package repository

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type graphSearchEvidenceRepository struct {
	db *gorm.DB
}

// NewGraphSearchEvidenceRepository constructs the scoped live-source projection.
func NewGraphSearchEvidenceRepository(db *gorm.DB) interfaces.GraphSearchEvidenceRepository {
	return &graphSearchEvidenceRepository{db: db}
}

func (r *graphSearchEvidenceRepository) GetGraphSourceChunks(
	ctx context.Context, tenantID uint64, kbID string, ids []string,
) ([]types.GraphSearchSourceChunk, error) {
	return r.getGraphSourceChunks(ctx, tenantID, kbID, ids, false)
}

func (r *graphSearchEvidenceRepository) GetGraphSourceMetadata(
	ctx context.Context, tenantID uint64, kbID string, ids []string,
) ([]types.GraphSearchSourceChunk, error) {
	return r.getGraphSourceChunks(ctx, tenantID, kbID, ids, true)
}

func (r *graphSearchEvidenceRepository) getGraphSourceChunks(
	ctx context.Context, tenantID uint64, kbID string, ids []string, metadataOnly bool,
) ([]types.GraphSearchSourceChunk, error) {
	limit := types.GraphBrowseMaxEvidence
	if !metadataOnly {
		limit = types.GraphSearchMaxEvidence
	}
	if tenantID == 0 || kbID == "" || len(ids) > limit {
		return nil, fmt.Errorf("invalid graph evidence scope or budget")
	}
	rows := []types.GraphSearchSourceChunk{}
	if len(ids) == 0 {
		return rows, nil
	}
	// Explicit soft-delete predicates are necessary: Table/Joins do not apply
	// the two models' GORM default scopes. SUBSTR is supported by every SQL
	// backend; fetching one extra character detects clipping without loading
	// unbounded content or doing a second, racy metadata-to-content lookup.
	query := r.db.WithContext(ctx).Table("chunks AS c")
	const fields = `c.id, c.tenant_id, c.knowledge_base_id, c.knowledge_id, SUBSTR(k.title, 1, 513) AS title`
	if metadataOnly {
		query = query.Select(fields)
	} else {
		query = query.Select(fields+`, c.chunk_index, c.start_at, c.end_at, SUBSTR(c.content, 1, ?) AS content`,
			types.GraphSearchMaxContentRunes+1)
	}
	err := query.
		Joins(`JOIN knowledges AS k ON k.id = c.knowledge_id
			AND k.tenant_id = c.tenant_id AND k.knowledge_base_id = c.knowledge_base_id`).
		Where("c.tenant_id = ? AND c.knowledge_base_id = ? AND c.id IN ?", tenantID, kbID, ids).
		Where("c.deleted_at IS NULL AND k.deleted_at IS NULL AND c.is_enabled = ?", true).
		Where("k.enable_status = ? AND k.parse_status <> ?", "enabled", types.ParseStatusDeleting).
		Where("(c.index_status = ? OR c.index_status = ?)", "ready", "").
		Order("c.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Content, rows[i].ContentTruncated = graphSearchClip(rows[i].Content, types.GraphSearchMaxContentRunes)
		rows[i].Title, rows[i].TitleTruncated = graphSearchClip(rows[i].Title, 512)
	}
	return rows, nil
}

func graphSearchClip(text string, limit int) (string, bool) {
	runes := []rune(text) // SQL has already bounded this allocation.
	if len(runes) > limit {
		return string(runes[:limit]), true
	}
	return text, false
}
