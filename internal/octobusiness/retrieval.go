package octobusiness

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
)

type retrievalTraceKey struct{}
type RetrievalTrace struct {
	mu                sync.Mutex
	succeeded, failed bool
	sources           []SourceCitation
}

type SourceCitation struct{ KnowledgeBaseID, URL, Path, Revision string }

func SourceCitations(ctx context.Context) []SourceCitation {
	trace, ok := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if !ok {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]SourceCitation(nil), trace.sources...)
}

// WithRetrievalTrace starts a turn-local trace. This is never persisted to chat
// history and model text cannot mark a failed retrieval as successful.
func WithRetrievalTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, retrievalTraceKey{}, &RetrievalTrace{})
}
func retrievalReady(ctx context.Context) bool {
	trace, ok := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if !ok {
		return false
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return trace.succeeded && !trace.failed
}

type trackedRetrieval struct{ types.Tool }

func TrackRetrieval(tool types.Tool) types.Tool {
	if tool == nil {
		return nil
	}
	// Registration may call this for every tool. Business/thinking/formatting
	// results are not evidence of knowledge retrieval and retain their original
	// optional capabilities without wrapping.
	switch tool.Name() {
	case "knowledge_search", "grep_chunks", "source_browse", "list_knowledge_chunks", "get_document_info", "wiki_search", "wiki_read_page", "wiki_read_source_doc":
		return &trackedRetrieval{Tool: tool}
	default:
		return tool
	}
}
func (t *trackedRetrieval) Cleanup(ctx context.Context) {
	if cleanable, ok := t.Tool.(types.Cleanable); ok {
		cleanable.Cleanup(ctx)
	}
}
func (t *trackedRetrieval) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	result, err := t.Tool.Execute(ctx, args)
	if trace, ok := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace); ok {
		trace.mu.Lock()
		if err != nil || result == nil || !result.Success {
			trace.failed = true
		} else {
			trace.succeeded = true
			if t.Tool.Name() == "source_browse" {
				var input struct {
					Action          string `json:"action"`
					KnowledgeBaseID string `json:"knowledge_base_id"`
				}
				var read types.SourceRead
				if json.Unmarshal(args, &input) == nil && input.Action == "read" && input.KnowledgeBaseID != "" && json.Unmarshal([]byte(result.Output), &read) == nil && read.SourceURL != "" && read.Path != "" {
					trace.sources = append(trace.sources, SourceCitation{KnowledgeBaseID: input.KnowledgeBaseID, URL: read.SourceURL, Path: read.Path, Revision: read.Revision})
				}
			}
		}
		trace.mu.Unlock()
	}
	return result, err
}
