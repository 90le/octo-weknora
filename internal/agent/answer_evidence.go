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
// a source read needs the private provenance marker attached by source_browse,
// while document evidence requires an actual returned passage or reader body.
func recordAnswerEvidenceFromStep(ctx context.Context, step types.AgentStep) {
	for _, call := range step.ToolCalls {
		if call.Result == nil || !call.Result.Success {
			continue
		}
		if call.Name == agenttools.ToolGitHubReleaseLookup && hasGitHubReleaseProvenance(call.Result) {
			answerevidence.RecordReleaseEvidence(ctx)
			continue
		}
		if call.Name == agenttools.ToolSourceBrowse && hasSourceBrowseReadProvenance(call.Result) {
			answerevidence.RecordSourceRead(ctx)
			continue
		}
		if hasDocumentEvidence(call.Name, call.Result.Output) {
			answerevidence.RecordDocumentEvidence(ctx)
		}
	}
}

// hasGitHubReleaseProvenance accepts only the typed, private marker attached
// by trusted release lookup code. A public tag in tool text, an arbitrary RAG
// chunk, or a source snapshot cannot set the latest-release evidence ledger.
func hasGitHubReleaseProvenance(result *types.ToolResult) bool {
	if result == nil || !result.Success || result.Data == nil {
		return false
	}
	raw, ok := result.Data[types.GitHubReleaseCitationDataKey]
	if !ok {
		return false
	}
	switch citation := raw.(type) {
	case types.GitHubReleaseCitation:
		return validGitHubReleaseCitation(citation)
	case *types.GitHubReleaseCitation:
		return citation != nil && validGitHubReleaseCitation(*citation)
	default:
		return false
	}
}

func validGitHubReleaseCitation(citation types.GitHubReleaseCitation) bool {
	// A verified "no published stable release" result has no tag or URL by
	// design. The data-source binding and checked timestamp still establish the
	// provenance of that negative latest-release result.
	return citation.KnowledgeBaseID != "" && citation.DataSourceID != "" &&
		citation.Repository != "" && !citation.CheckedAt.IsZero()
}

func hasSourceBrowseReadProvenance(result *types.ToolResult) bool {
	if result == nil || !result.Success || result.Data == nil {
		return false
	}
	raw, ok := result.Data[types.SourceBrowseCitationDataKey]
	if !ok {
		return false
	}
	switch citation := raw.(type) {
	case types.SourceBrowseCitation:
		return citation.KnowledgeBaseID != "" && citation.Path != ""
	case *types.SourceBrowseCitation:
		return citation != nil && citation.KnowledgeBaseID != "" && citation.Path != ""
	default:
		return false
	}
}

// hasDocumentEvidence deliberately treats a zero-hit search as no evidence.
// Tool success merely proves that a request ran; it does not prove that a
// release, integration, or capability was found.
func hasDocumentEvidence(toolName, output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}
	switch toolName {
	case agenttools.ToolKnowledgeSearch, agenttools.ToolGrepChunks, agenttools.ToolListKnowledgeChunks:
		lower := strings.ToLower(output)
		// Native renderers normally add attributes after <chunk>/<faq>, but
		// accept the minimal valid tags too so evidence classification follows
		// the returned result rather than one presentation detail.
		return strings.Contains(lower, "<chunk") || strings.Contains(lower, "<faq")
	case agenttools.ToolGetDocumentInfo:
		// The normal knowledge-id path exposes document metadata only. FAQ
		// entries additionally include their answer body, which is actual
		// document evidence; a title, path, or metadata record is not.
		return strings.Contains(output, "Answers:")
	case agenttools.ToolWikiReadPage, agenttools.ToolWikiReadSourceDoc:
		return true
	case agenttools.ToolWebFetch:
		return strings.Contains(output, "Content (untrusted evidence):")
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
