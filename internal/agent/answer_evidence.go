package agent

import (
	"context"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/answerevidence"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// recordAnswerEvidenceFromStep promotes only trusted, completed tool outcomes
// into the turn-local evidence contract. The model never controls these flags:
// source search/read and release lookup each need private markers attached by
// their trusted tools, while document evidence requires an actual returned
// passage or reader body.
func recordAnswerEvidenceFromStep(ctx context.Context, step types.AgentStep) {
	for _, call := range step.ToolCalls {
		if call.Result == nil || !call.Result.Success {
			continue
		}
		if call.Name == agenttools.ToolGitHubReleaseLookup {
			if hasGitHubReleaseLookupAudit(call.Result) {
				answerevidence.RecordReleaseLookup(ctx)
			}
			if citation, ok := githubReleaseCitation(call.Result); ok {
				answerevidence.RecordReleaseEvidence(ctx, citation.Repository)
			}
			continue
		}
		if call.Name == agenttools.ToolSourceBrowse {
			if search, ok := sourceBrowseSearchAudit(call.Result); ok {
				if search.Global {
					answerevidence.RecordSourceSearch(ctx, search.Complete, search.Matched)
				} else {
					answerevidence.RecordSourceSearch(ctx, search.Complete, search.Matched, search.Repository)
				}
			}
			if citation, ok := sourceBrowseReadCitation(call.Result); ok {
				answerevidence.RecordSourceRead(ctx, citation.Repository)
			}
			continue
		}
		if hasDocumentEvidence(call.Name, call.Result) {
			answerevidence.RecordDocumentEvidence(ctx)
		}
	}
}

// githubReleaseCitation accepts only the typed, private marker attached by
// trusted release lookup code. A public tag in tool text, an arbitrary RAG
// chunk, or a source snapshot cannot set the latest-release evidence ledger.
func githubReleaseCitation(result *types.ToolResult) (types.GitHubReleaseCitation, bool) {
	if result == nil || !result.Success || result.Data == nil {
		return types.GitHubReleaseCitation{}, false
	}
	raw, ok := result.Data[types.GitHubReleaseCitationDataKey]
	if !ok {
		return types.GitHubReleaseCitation{}, false
	}
	switch citation := raw.(type) {
	case types.GitHubReleaseCitation:
		return citation, validGitHubReleaseCitation(citation)
	case *types.GitHubReleaseCitation:
		if citation != nil {
			return *citation, validGitHubReleaseCitation(*citation)
		}
	default:
	}
	return types.GitHubReleaseCitation{}, false
}

func hasGitHubReleaseLookupAudit(result *types.ToolResult) bool {
	if result == nil || !result.Success || result.Data == nil {
		return false
	}
	raw, ok := result.Data[types.GitHubReleaseLookupDataKey]
	if !ok {
		return false
	}
	switch audit := raw.(type) {
	case types.GitHubReleaseLookupAudit:
		return audit.Repository != ""
	case *types.GitHubReleaseLookupAudit:
		return audit != nil && audit.Repository != ""
	default:
		return false
	}
}

func validGitHubReleaseCitation(citation types.GitHubReleaseCitation) bool {
	// Only a positive result from GitHub's dedicated latest-release endpoint can
	// unlock a latest-version conclusion. Bounded history, tags, and the
	// no-stable fallback remain useful context but cannot establish currentness.
	return citation.KnowledgeBaseID != "" && citation.DataSourceID != "" &&
		citation.Repository != "" && citation.TagName != "" && citation.URL != "" &&
		!citation.PublishedAt.IsZero() && !citation.CheckedAt.IsZero()
}

func hasSourceBrowseReadProvenance(result *types.ToolResult) bool {
	_, ok := sourceBrowseReadCitation(result)
	return ok
}

func sourceBrowseReadCitation(result *types.ToolResult) (types.SourceBrowseCitation, bool) {
	if result == nil || !result.Success || result.Data == nil {
		return types.SourceBrowseCitation{}, false
	}
	raw, ok := result.Data[types.SourceBrowseCitationDataKey]
	if !ok {
		return types.SourceBrowseCitation{}, false
	}
	switch citation := raw.(type) {
	case types.SourceBrowseCitation:
		return citation, citation.KnowledgeBaseID != "" && citation.Path != ""
	case *types.SourceBrowseCitation:
		if citation != nil && citation.KnowledgeBaseID != "" && citation.Path != "" {
			return *citation, true
		}
		return types.SourceBrowseCitation{}, false
	default:
		return types.SourceBrowseCitation{}, false
	}
}

func sourceBrowseSearchAudit(result *types.ToolResult) (types.SourceBrowseSearchAudit, bool) {
	if result == nil || !result.Success || result.Data == nil {
		return types.SourceBrowseSearchAudit{}, false
	}
	raw, ok := result.Data[types.SourceBrowseSearchDataKey]
	if !ok {
		return types.SourceBrowseSearchAudit{}, false
	}
	switch audit := raw.(type) {
	case types.SourceBrowseSearchAudit:
		return audit, true
	case *types.SourceBrowseSearchAudit:
		if audit != nil {
			return *audit, true
		}
	}
	return types.SourceBrowseSearchAudit{}, false
}

// hasDocumentEvidence deliberately treats a zero-hit search as no evidence.
// Tool success merely proves that a request ran; it does not prove that a
// release, integration, or capability was found.
func hasDocumentEvidence(toolName string, result *types.ToolResult) bool {
	if result == nil || !result.Success || strings.TrimSpace(result.Output) == "" || result.Data == nil {
		return false
	}
	switch toolName {
	case agenttools.ToolKnowledgeSearch:
		return positiveEvidenceCount(result.Data["count"]) &&
			(strings.Contains(result.Output, "<content>") || strings.Contains(result.Output, "<answer>"))
	case agenttools.ToolGrepChunks:
		return positiveEvidenceCount(result.Data["result_count"]) && strings.Contains(result.Output, "<match_snippet>")
	case agenttools.ToolListKnowledgeChunks:
		return positiveEvidenceCount(result.Data["fetched_chunks"]) && strings.Contains(result.Output, "<content>")
	case agenttools.ToolGetDocumentInfo:
		// A document title is only navigation. An FAQ answer is a body, and
		// its typed data cannot be forged by an unrelated document's text.
		if !strings.Contains(result.Output, "Answers:") {
			return false
		}
		if docs, ok := result.Data["documents"].([]map[string]interface{}); ok {
			for _, doc := range docs {
				if answers, ok := doc["faq_answers"].([]string); ok && len(answers) > 0 {
					return true
				}
			}
		}
		return false
	case agenttools.ToolWikiReadPage:
		found, ok := result.Data["found_kbs"].(map[string][]string)
		return ok && len(found) > 0 && strings.Contains(result.Output, "<wiki_page>")
	case agenttools.ToolWikiReadSourceDoc:
		return positiveEvidenceCount(result.Data["fetched_chunks"])
	case agenttools.ToolWebFetch:
		return positiveEvidenceCount(result.Data["successful_count"]) &&
			strings.Contains(result.Output, "Content (untrusted evidence):")
	default:
		return false
	}
}

func positiveEvidenceCount(value interface{}) bool {
	switch count := value.(type) {
	case int:
		return count > 0
	case int64:
		return count > 0
	case float64:
		return count > 0
	default:
		return false
	}
}

func (e *AgentEngine) completeWithEvidenceFallback(
	ctx context.Context,
	state *types.AgentState,
	step types.AgentStep,
	sessionID string,
) {
	fallback := answerevidence.FallbackReply(ctx)
	if fallback == "" {
		return
	}
	logger.Warnf(ctx, "[Agent] Evidence contract stopped an unverified %s conclusion", answerevidence.IntentFromContext(ctx))
	common.PipelineWarn(ctx, "Agent", "answer_evidence_fallback", map[string]interface{}{
		"intent": string(answerevidence.IntentFromContext(ctx)),
	})
	state.FinalAnswer = fallback
	state.IsComplete = true
	state.RoundSteps = append(state.RoundSteps, step)
	answerID := generateEventID("answer")
	_ = e.eventBus.Emit(ctx, event.Event{
		ID:        answerID,
		Type:      event.EventAgentFinalAnswer,
		SessionID: sessionID,
		Data: event.AgentFinalAnswerData{
			Content:    fallback,
			Done:       false,
			IsFallback: true,
		},
	})
	e.closeAnswerStream(ctx, sessionID, answerID)
}
