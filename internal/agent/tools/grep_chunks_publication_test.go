package tools

import (
	"github.com/Tencent/WeKnora/internal/types"
	"testing"
)

func TestGrepPublicIMOmitsUnpublishedDocuments(t *testing.T) {
	documents := []*types.Knowledge{
		{ID: "published", EnableStatus: "enabled"},
		{ID: "disabled", EnableStatus: "disabled"},
		{ID: "draft", EnableStatus: "enabled", Type: types.KnowledgeTypeManual, Metadata: types.JSON(`{"status":"draft"}`)},
		{ID: "candidate", EnableStatus: "enabled", Channel: "local_folder", Metadata: types.JSON(`{"sync_target_external_id":"pending"}`)},
	}
	var chunks []chunkWithTitle
	for _, id := range []string{"published", "disabled", "draft", "candidate", "missing"} {
		chunks = append(chunks, chunkWithTitle{Chunk: types.Chunk{KnowledgeID: id}})
	}
	got := publishedGrepChunks(chunks, documents)
	if len(got) != 1 || got[0].KnowledgeID != "published" {
		t.Fatalf("unexpected public chunks: %+v", got)
	}
}
