package localfolder

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createOCRWarningTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE knowledges (
  id TEXT PRIMARY KEY, tenant_id BIGINT, knowledge_base_id TEXT, metadata TEXT, deleted_at TIMESTAMP
)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE knowledge_processing_spans (
  id INTEGER PRIMARY KEY, knowledge_id TEXT, name TEXT, status TEXT, attempt INTEGER, updated_at TIMESTAMP
)`).Error)
}

func TestFileStatesTrackUpdatesAndAreScopedToSourceAndSelection(t *testing.T) {
	r := registryWithSpace(t, t.TempDir())
	createOCRWarningTables(t, r.db)
	migration, err := os.ReadFile("../../../../migrations/sqlite/000049_local_source_file_states.up.sql")
	require.NoError(t, err)
	require.NoError(t, r.db.Exec(string(migration)).Error)
	c := NewConnector(r)
	ctx := context.Background()
	selection := strings.Repeat("a", 64)
	state := types.LocalSourceFileState{
		Selection:   selection,
		Path:        "chapter/doc.pdf",
		Status:      "failed",
		Reason:      "local_size",
		HasPrevious: true,
	}
	require.NoError(t, c.RecordFileState(ctx, 7, "source", state))
	state.Status, state.Reason = "indexed", ""
	require.NoError(t, c.RecordFileState(ctx, 7, "source", state))
	require.NoError(t, c.RecordFileState(ctx, 8, "source", state))
	page, err := c.ListFileStates(ctx, 7, "source", selection, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "indexed", page.Items[0].Status)
	require.EqualValues(t, 1, page.Counts["indexed"])
	page, err = c.ListFileStates(ctx, 7, "other", selection, "", 0)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	page, err = c.ListFileStates(ctx, 7, "source", strings.Repeat("b", 64), "", 0)
	require.NoError(t, err)
	require.Empty(t, page.Items)
}

func TestFileStateOCRWarningsUseLatestCanonicalScopedDocument(t *testing.T) {
	r := registryWithSpace(t, t.TempDir())
	createOCRWarningTables(t, r.db)
	migration, err := os.ReadFile("../../../../migrations/sqlite/000049_local_source_file_states.up.sql")
	require.NoError(t, err)
	require.NoError(t, r.db.Exec(string(migration)).Error)
	sourceUsingRoot(t, r, "source", 7, "root")
	c := NewConnector(r)
	selection := strings.Repeat("a", 64)
	require.NoError(t, c.RecordFileState(context.Background(), 7, "source", types.LocalSourceFileState{
		Selection: selection, Path: "chapter/doc.pdf", Status: "indexed", SyncedVersion: "v1",
	}))
	insertKnowledge := func(id string, tenant int, source, root, version string, candidate bool) {
		meta := map[string]string{
			"datasource_id": source, "source_resource_id": root,
			"source_path": "chapter/doc.pdf", "source_version": version,
		}
		if candidate {
			meta["sync_target_external_id"] = "candidate"
		}
		b, marshalErr := json.Marshal(meta)
		require.NoError(t, marshalErr)
		require.NoError(t, r.db.Exec(
			"INSERT INTO knowledges(id, tenant_id, knowledge_base_id, metadata) VALUES (?, ?, 'kb', ?)",
			id, tenant, string(b),
		).Error)
	}
	insertSpan := func(id, knowledge, name, status string, attempt int) {
		require.NoError(t, r.db.Exec(
			"INSERT INTO knowledge_processing_spans(knowledge_id, name, status, attempt, updated_at) "+
				"VALUES (?, ?, ?, ?, ?)",
			knowledge, name, status, attempt, id,
		).Error)
	}
	insertKnowledge("canonical", 7, "source", "root", "v1", false)
	insertSpan("1", "canonical", "multimodal.image[0].ocr", "failed", 1)
	insertSpan("2", "canonical", "multimodal.image[0].ocr", "done", 2)
	insertSpan("3", "canonical", "multimodal.image[1].ocr", "failed", 1)
	insertKnowledge("candidate", 7, "source", "root", "v1", true)
	insertSpan("4", "candidate", "multimodal.image[2].ocr", "failed", 1)
	insertKnowledge("other-version", 7, "source", "root", "v2", false)
	insertSpan("5", "other-version", "multimodal.image[3].ocr", "failed", 1)
	insertKnowledge("other-tenant", 8, "source", "root", "v1", false)
	insertSpan("6", "other-tenant", "multimodal.image[4].ocr", "failed", 1)
	insertKnowledge("other-root", 7, "source", "other", "v1", false)
	insertSpan("7", "other-root", "multimodal.image[5].ocr", "failed", 1)
	page, err := c.ListFileStates(context.Background(), 7, "source", selection, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.EqualValues(t, 1, page.Items[0].OCRFailedImages)
}
