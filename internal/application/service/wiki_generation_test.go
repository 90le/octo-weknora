package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWikiGenerationRecoversArchivedDocumentFailures(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_bases (
		id TEXT PRIMARY KEY, tenant_id INTEGER, deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE knowledges (id TEXT PRIMARY KEY, tenant_id INTEGER, knowledge_base_id TEXT,
		title TEXT, parse_status TEXT, enable_status TEXT, deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE wiki_pages (tenant_id INTEGER, knowledge_base_id TEXT,
		status TEXT, page_type TEXT, source_refs JSON)`).Error)
	require.NoError(t, db.AutoMigrate(
		&types.TaskPendingOp{}, &types.TaskDeadLetter{}, &types.KnowledgeProcessingSpan{},
	))
	require.NoError(t, db.Exec(`INSERT INTO knowledge_bases (id, tenant_id) VALUES ('kb-1', 7), ('kb-2', 8)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO knowledges VALUES
		('a', 7, 'kb-1', 'Source A', 'completed', 'enabled', NULL),
		('b', 7, 'kb-1', 'Source B', 'completed', 'enabled', NULL),
		('c', 7, 'kb-1', 'Source C', 'completed', 'enabled', NULL),
		('d', 7, 'kb-1', 'Source D', 'completed', 'disabled', NULL),
		('e', 8, 'kb-2', 'Other workspace', 'completed', 'enabled', NULL),
		('f', 7, 'kb-1', 'Deleted', 'completed', 'enabled', CURRENT_TIMESTAMP)`).Error)
	archive := repository.NewTaskDeadLetterRepository(db)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		tenant, kb := uint64(7), "kb-1"
		if id == "e" {
			tenant, kb = 8, "kb-2"
		}
		payload, err := json.Marshal(WikiPendingOp{Op: WikiOpIngest, KnowledgeID: id, Language: "zh-CN", Attempt: 2})
		require.NoError(t, err)
		require.NoError(t, archive.Insert(ctx, &types.TaskDeadLetter{
			TenantID: tenant, ScopeID: kb, Scope: wikiTaskScope, TaskType: wikiTaskType,
			RelatedID: id, FailCount: 6, Payload: payload,
		}))
	}
	require.NoError(t, db.Exec(`INSERT INTO wiki_pages VALUES (7, 'kb-1', 'published', 'summary', '["c|Source C"]'),
		(8, 'kb-2', 'published', 'summary', '["b"]')`).Error)
	require.NoError(t, db.Create(&types.KnowledgeProcessingSpan{
		KnowledgeID: "a", Name: "postprocess.wiki", SpanID: "old-a", Status: types.SpanStatusFailed,
		ErrorCode: "EXTRACT_FAILED", ErrorMessage: "upstream HTTP 504 detail",
	}).Error)

	kb := &wikiLookupKBService{kb: &types.KnowledgeBase{
		ID: "kb-1", TenantID: 7, SummaryModelID: "model", IndexingStrategy: types.IndexingStrategy{WikiEnabled: true},
	}}
	queue := &wikiGuardTaskQueue{}
	svc := NewWikiGenerationService(db, queue, wikiLookupModelService{}, kb)
	page, err := svc.List(ctx, 7, "kb-1", "")
	require.NoError(t, err)
	require.Len(t, page.Tasks, 4)
	require.Equal(t, "gateway_timeout", page.Tasks[0].ErrorCode)
	require.Equal(t, "published", page.Tasks[2].Status)
	require.Equal(t, "unavailable", page.Tasks[3].Status)

	first, err := svc.Retry(ctx, 7, "kb-1", []string{"a"})
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, first.Queued)
	second, err := svc.Retry(ctx, 7, "kb-1", []string{"a", "a", "b", "c", "d", "e", "f"})
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, second.Queued)
	require.Equal(t, 5, second.Skipped)
	require.Len(t, queue.tasks, 2)
	var pending []types.TaskPendingOp
	require.NoError(t, db.Order("id").Find(&pending).Error)
	require.Len(t, pending, 2)
	var op WikiPendingOp
	require.NoError(t, json.Unmarshal(pending[0].Payload, &op))
	require.True(t, op.ManualRetry)
	require.Zero(t, op.Attempt)
	require.Equal(t, "zh-CN", op.Language)

	// Read progress through a fresh service instance. Persisted rows survive a restart.
	svc = NewWikiGenerationService(db, queue, wikiLookupModelService{}, kb)
	page, err = svc.List(ctx, 7, "kb-1", "")
	require.NoError(t, err)
	require.Equal(t, "queued", page.Tasks[0].Status)
	require.Equal(t, "queued", page.Tasks[1].Status)
	require.NoError(t, db.Model(&pending[0]).Update("claimed_at", time.Now()).Error)
	page, err = svc.List(ctx, 7, "kb-1", "")
	require.NoError(t, err)
	require.Equal(t, "running", page.Tasks[0].Status)

	// Exhaustion in the real settlement path preserves a current failure and
	// never releases a parsing slot for the retry.
	worker := &wikiIngestService{pendingRepo: repository.NewTaskPendingOpsRepository(db), deadLetterRepo: archive}
	op.dbID = pending[0].ID
	for attempt := 0; attempt <= wikiMaxFailRetries; attempt++ {
		require.NoError(t, worker.requeueFailedOps(ctx,
			WikiIngestPayload{TenantID: 7, KnowledgeBaseID: "kb-1"}, []WikiPendingOp{op},
		))
	}
	page, err = svc.List(ctx, 7, "kb-1", "")
	require.NoError(t, err)
	require.Equal(t, "failed", page.Tasks[0].Status)
	require.NotNil(t, page.Tasks[0].FailedAt)
	require.NoError(t, db.Exec(`INSERT INTO wiki_pages VALUES (7, 'kb-1', 'published', 'summary', '["b"]')`).Error)
	require.NoError(t, db.Delete(&pending[1]).Error)
	page, err = svc.List(ctx, 7, "kb-1", "")
	require.NoError(t, err)
	require.Equal(t, "published", page.Tasks[1].Status)
}
