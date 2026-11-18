package repository

import (
	"encoding/json"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// WikiGenerationRow combines the latest archived ingest failure with the
// current document, queue, and Wiki publication state.
type WikiGenerationRow struct {
	KnowledgeID  string
	Title        string
	ParseStatus  string
	EnableStatus string
	FailedAt     time.Time
	FailCount    int
	Failure      json.RawMessage
	Published    bool
	PendingID    int64
	EnqueuedAt   *time.Time
	ClaimedAt    *time.Time
	RetryCount   int
	SpanStatus   string
	SpanStarted  *time.Time
	SpanCode     string
	SpanError    string
}

func ListWikiGenerationRows(db *gorm.DB, tenantID uint64, kbID string, taskType string, after string, ids []string, limit int) ([]WikiGenerationRow, error) {
	scope := db.Session(&gorm.Session{NewDB: true})
	// Both supported databases return a scalar from ->>. Scope filtering
	// excludes queue-level snapshots and retract failures.
	failures := scope.Model(&types.TaskDeadLetter{}).
		Select("MAX(id) AS id").
		Where("tenant_id = ? AND scope = ? AND scope_id = ? AND task_type = ?",
			tenantID, types.TaskScopeKnowledgeBase, kbID, taskType).
		Where("related_id > ? AND payload ->> 'op' = 'ingest'", after).
		Group("related_id")
	if len(ids) > 0 {
		failures = failures.Where("related_id IN ?", ids)
	}
	refs := "json_each(page.source_refs) ref"
	refValue := "ref.value"
	if db.Dialector.Name() == "postgres" {
		refs = "jsonb_array_elements_text(page.source_refs) AS ref(value)"
	}
	published := `EXISTS (
		SELECT 1 FROM wiki_pages page, ` + refs + `
		WHERE page.tenant_id = k.tenant_id AND page.knowledge_base_id = k.knowledge_base_id
			AND page.status = 'published' AND page.page_type <> 'index'
			AND (` + refValue + ` = k.id OR SUBSTR(` + refValue + `, 1, LENGTH(k.id) + 1) = k.id || '|')
	)`
	var rows []WikiGenerationRow
	err := scope.Table("task_dead_letters AS failure").
		Select(`k.id AS knowledge_id, k.title, k.parse_status, k.enable_status,
			failure.failed_at, failure.fail_count, failure.payload AS failure, `+published+` AS published,
			COALESCE(pending.id, 0) AS pending_id, pending.enqueued_at, pending.claimed_at,
			COALESCE(pending.fail_count, 0) AS retry_count,
			COALESCE(span.status, '') AS span_status, span.started_at AS span_started,
			COALESCE(span.error_code, '') AS span_code, COALESCE(span.error_message, '') AS span_error`).
		Joins("JOIN (?) AS latest ON latest.id = failure.id", failures).
		Joins(`JOIN knowledges k ON k.id = failure.related_id AND k.tenant_id = failure.tenant_id
			AND k.knowledge_base_id = failure.scope_id AND k.deleted_at IS NULL`).
		Joins(`LEFT JOIN task_pending_ops pending ON pending.id = (
			SELECT MAX(p.id) FROM task_pending_ops p WHERE p.tenant_id = failure.tenant_id
				AND p.scope = failure.scope AND p.scope_id = failure.scope_id AND p.task_type = failure.task_type
				AND p.dedup_key = k.id
		)`).
		Joins(`LEFT JOIN knowledge_processing_spans span ON span.id = (
			SELECT MAX(s.id) FROM knowledge_processing_spans s
			WHERE s.knowledge_id = k.id AND s.name = 'postprocess.wiki'
		)`).
		Order("k.id").Limit(limit).Scan(&rows).Error
	return rows, err
}
