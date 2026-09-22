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
	citationsEnabled  bool
}

// SourceCitation is turn-local transport metadata derived only from trusted
// tool provenance. It is not a model-provided citation tag and is never stored
// with chat history. OfficialRelease tells IM rendering that the release URL
// was system-retrieved for this turn and may therefore be appended even when a
// model forgot to render an inline source handle.
type SourceCitation struct {
	KnowledgeBaseID string
	URL             string
	Path            string
	Revision        string
	Title           string
	OfficialRelease bool
}

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
	return context.WithValue(ctx, retrievalTraceKey{}, &RetrievalTrace{citationsEnabled: true})
}

// SetCitationRenderingEnabled records the resolved agent setting on the
// existing turn trace. IM owns the trace for the entire delivery lifecycle,
// while AgentQA resolves the effective setting later in the pipeline.
func SetCitationRenderingEnabled(ctx context.Context, enabled bool) {
	trace, ok := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if !ok || trace == nil {
		return
	}
	trace.mu.Lock()
	trace.citationsEnabled = enabled
	trace.mu.Unlock()
}

// CitationRenderingEnabled defaults to true to preserve existing Octo source
// rendering for contexts that do not carry an IM retrieval trace.
func CitationRenderingEnabled(ctx context.Context) bool {
	trace, ok := ctx.Value(retrievalTraceKey{}).(*RetrievalTrace)
	if !ok || trace == nil {
		return true
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return trace.citationsEnabled
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
	case "knowledge_search", "grep_chunks", "source_browse", "list_knowledge_chunks", "get_document_info", "wiki_search", "wiki_read_page", "wiki_read_source_doc", "github_release_lookup":
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
		toolName := t.Tool.Name()
		isKnowledgeRetrieval := toolName != "github_release_lookup"
		if err != nil || result == nil || !result.Success {
			if isKnowledgeRetrieval {
				trace.failed = true
			}
		} else {
			if isKnowledgeRetrieval {
				trace.succeeded = true
			}
			switch toolName {
			case "source_browse":
				var input struct {
					Action string `json:"action"`
				}
				if json.Unmarshal(args, &input) == nil && input.Action == "read" {
					if raw, ok := result.Data[types.SourceBrowseCitationDataKey]; ok {
						if citation, ok := raw.(types.SourceBrowseCitation); ok && citation.KnowledgeBaseID != "" && citation.URL != "" && citation.Path != "" {
							trace.sources = append(trace.sources, SourceCitation{KnowledgeBaseID: citation.KnowledgeBaseID, URL: citation.URL, Path: citation.Path, Revision: citation.Revision})
						}
					}
				}
			case "github_release_lookup":
				if citation, ok := githubReleaseCitation(result); ok {
					trace.sources = append(trace.sources, SourceCitation{
						KnowledgeBaseID: citation.KnowledgeBaseID,
						URL:             citation.URL,
						Title:           citation.Repository + " · " + citation.TagName,
						OfficialRelease: true,
					})
				}
			}
		}
		trace.mu.Unlock()
	}
	return result, err
}

func githubReleaseCitation(result *types.ToolResult) (types.GitHubReleaseCitation, bool) {
	if result == nil || !result.Success || result.Data == nil {
		return types.GitHubReleaseCitation{}, false
	}
	raw, ok := result.Data[types.GitHubReleaseCitationDataKey]
	if !ok {
		return types.GitHubReleaseCitation{}, false
	}
	var citation types.GitHubReleaseCitation
	switch value := raw.(type) {
	case types.GitHubReleaseCitation:
		citation = value
	case *types.GitHubReleaseCitation:
		if value == nil {
			return types.GitHubReleaseCitation{}, false
		}
		citation = *value
	default:
		return types.GitHubReleaseCitation{}, false
	}
	if citation.KnowledgeBaseID == "" || citation.DataSourceID == "" || citation.Repository == "" || citation.TagName == "" || citation.URL == "" ||
		citation.PublishedAt.IsZero() || citation.CheckedAt.IsZero() {
		return types.GitHubReleaseCitation{}, false
	}
	return citation, true
}
