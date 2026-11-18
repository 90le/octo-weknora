package octobusiness

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
)

// QualityTurnKey names server-produced metadata in the durable Octo inbox.
const QualityTurnKey = "octo_quality_turn_v1"

// QualitySource identifies a retrieved Wiki page or a cited document chunk.
type QualitySource struct {
	Library  string `json:"library"`
	WikiSlug string `json:"wiki_slug,omitempty"`
	ChunkID  string `json:"chunk_id,omitempty"`
}

// QualityTurn records a completed question-answer attempt, including retrieval health.
type QualityTurn struct {
	Status  string          `json:"status"`
	Sources []QualitySource `json:"sources,omitempty"`
}

// RecordQualityTurn captures evidence from verified tool results and final citations.
func RecordQualityTurn(ctx context.Context, refs []*types.SearchResult, failed bool) {
	trace, _ := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	turn := &QualityTurn{Status: "answered"}
	switch {
	case failed:
		turn.Status = "failed"
	case trace.failed:
		turn.Status = "retrieval_warning"
	case trace.succeeded:
		turn.Status = "retrieved"
	}
	for _, ref := range refs {
		if ref != nil && ref.ID != "" && ref.KnowledgeBaseID != "" && len(turn.Sources) < 12 {
			turn.Sources = append(turn.Sources, QualitySource{Library: ref.KnowledgeBaseID, ChunkID: ref.ID})
		}
	}
	for _, src := range trace.wikiRead {
		if len(turn.Sources) < 12 {
			turn.Sources = append(turn.Sources, src)
		}
	}
	trace.quality = turn
}

// EncodedQualityTurn returns optional server-produced inbox metadata.
func EncodedQualityTurn(ctx context.Context) string {
	trace, _ := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if trace == nil {
		return ""
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.quality == nil {
		return ""
	}
	encoded, _ := json.Marshal(trace.quality)
	return string(encoded)
}
