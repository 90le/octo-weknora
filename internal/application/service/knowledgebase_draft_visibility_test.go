package service

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"testing"
)

func TestDraftKnowledgeVisibility(t *testing.T) {
	if isPublishedSearchKnowledge(nil) {
		t.Fatal("nil visible")
	}
	k := &types.Knowledge{Type: "manual", EnableStatus: "enabled"}
	if isPublishedSearchKnowledge(k) {
		t.Fatal("missing manual metadata visible")
	}
	if err := k.SetManualMetadata(types.NewManualKnowledgeMetadata("published text", "publish", 1)); err != nil {
		t.Fatal(err)
	}
	if !isPublishedSearchKnowledge(k) {
		t.Fatal("published hidden")
	}
	k.EnableStatus = "disabled"
	if isPublishedSearchKnowledge(k) {
		t.Fatal("disabled visible")
	}
	k.EnableStatus = "enabled"
	if err := k.SetManualMetadata(types.NewManualKnowledgeMetadata("draft text", "draft", 2)); err != nil {
		t.Fatal(err)
	}
	if isPublishedSearchKnowledge(k) {
		t.Fatal("draft visible despite stale enabled flag")
	}
	f := &types.Knowledge{Type: "file", EnableStatus: "enabled"}
	if !isPublishedSearchKnowledge(f) {
		t.Fatal("enabled file hidden")
	}
}

func TestDraftExcludedFromPrimaryAndEnrichmentResults(t *testing.T) {
	service := &knowledgeBaseService{}
	pub := &types.Knowledge{ID: "pub", Type: "file", EnableStatus: "enabled"}
	draft := &types.Knowledge{ID: "draft", Type: "manual", EnableStatus: "disabled"}
	if err := draft.SetManualMetadata(types.NewManualKnowledgeMetadata("draft text", "draft", 1)); err != nil {
		t.Fatal(err)
	}
	live := &types.Chunk{ID: "live", KnowledgeID: "pub", ChunkType: types.ChunkTypeText, Content: "visible", IsEnabled: true}
	hidden := &types.Chunk{ID: "hidden", KnowledgeID: "draft", ChunkType: types.ChunkTypeText, Content: "not visible", IsEnabled: true}
	inputs := []*types.IndexWithScore{{ChunkID: "live", KnowledgeID: "pub", Score: 1}, {ChunkID: "hidden", KnowledgeID: "draft", Score: 1}}
	idx := service.buildChunkIndex(inputs)
	// isSearchableChunk checks chunk type, not only the index's old enable flag.
	for _, enrich := range []bool{false, true} {
		got := service.assembleSearchResults(context.Background(), inputs, map[string]*types.Chunk{"live": live, "hidden": hidden}, map[string]*types.Knowledge{"pub": pub, "draft": draft}, idx, !enrich)
		if len(got) != 1 || got[0].KnowledgeID != "pub" {
			t.Fatalf("expected only published result, got %v", got)
		}
		for _, row := range got {
			if row.KnowledgeID == "draft" {
				t.Fatal("draft escaped assembly")
			}
		}
	}
}

func TestGitHubCandidateExcludedUntilAdopted(t *testing.T) {
	s := &knowledgeBaseService{}
	k := &types.Knowledge{ID: "new", Type: "file", Channel: types.ConnectorTypeGitHub, EnableStatus: "enabled", Metadata: types.JSON(`{"sync_target_external_id":"canonical","external_id":"pending"}`)}
	c := &types.Chunk{ID: "chunk", KnowledgeID: k.ID, ChunkType: types.ChunkTypeText, Content: "candidate", IsEnabled: true}
	input := []*types.IndexWithScore{{ChunkID: c.ID, KnowledgeID: k.ID, Score: 1}}
	for _, primary := range []bool{true, false} {
		got := s.assembleSearchResults(context.Background(), input, map[string]*types.Chunk{c.ID: c}, map[string]*types.Knowledge{k.ID: k}, s.buildChunkIndex(input), primary)
		if len(got) != 0 {
			t.Fatal("indexed candidate exposed before adoption")
		}
	}
	k.Metadata = types.JSON(`{"external_id":"canonical"}`)
	if !isPublishedSearchKnowledge(k) {
		t.Fatal("adopted repository document hidden")
	}
}
