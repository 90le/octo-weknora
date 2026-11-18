package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type issueEvidence struct {
	Question          string `json:"question,omitempty"`
	QuestionMessageID string `json:"question_message_id,omitempty"`
	Answer            string `json:"answer,omitempty"`
	AnswerMessageID   string `json:"answer_message_id,omitempty"`
}

// The inbox has a durable native quote, including the Bot's own reply anchor.
// Follow that anchor back to admitted input. The outer feedback text is not
// the original question and cannot be used as a replay prompt.
func (s *Service) issueEvidence(ctx context.Context, issue *Issue) (*issueEvidence, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console || p.TenantID != issue.TenantID {
		return nil, ErrDenied
	}
	if _, err = s.checkKB(ctx, p, issue.KnowledgeBaseID, false); err != nil {
		return nil, err
	}
	if issue.IsDirect || issue.GroupID == "" || issue.MessageID == "" {
		return nil, nil
	}
	inbox := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).
		Table("octo_message_inbox").Where("tenant_id = ? AND channel_id = ?", p.TenantID, issue.ChannelID)
	var row evidenceInbox
	if err := inbox.Session(&gorm.Session{}).Where("message_id = ?", issue.MessageID).Take(&row).Error; err != nil {
		return nil, ignoreMissingEvidence(err)
	}
	feedback := row.message(issue)
	if feedback != nil && issue.IdempotencyKey == qualityIssueKey(issue) {
		question := feedback.Content
		if clean, ok := types.OctoAddressedQuestion(feedback.Extra); ok {
			question = clean
		}
		answer := ""
		if row.State == "delivered" {
			answer = limitQualityText(row.Reply, 8000)
		}
		return &issueEvidence{
			Question:          limitQualityText(strings.TrimSpace(question), 4000),
			QuestionMessageID: feedback.Extra["octo_message_id"], Answer: answer,
		}, nil
	}
	if feedback == nil || feedback.Quote == nil || !feedback.Quote.IsBotMessage ||
		!validText(feedback.Quote.MessageID, 32) || !validText(feedback.Quote.SenderID, 128) ||
		!strings.HasPrefix(issue.MessageID, "octo:"+feedback.Quote.SenderID+":") {
		return nil, nil
	}
	evidence := &issueEvidence{Answer: feedback.Quote.Content, AnswerMessageID: feedback.Quote.MessageID}
	originalID := feedback.originalQuestionID()
	if originalID == "" || originalID == feedback.Extra["octo_message_id"] {
		return evidence, nil
	}
	var original evidenceInbox
	id := "octo:" + feedback.Quote.SenderID + ":" + originalID
	if err := inbox.Where("message_id = ? AND state = 'delivered'", id).Take(&original).Error; err != nil {
		return evidence, ignoreMissingEvidence(err)
	}
	src := original.message(issue)
	if src == nil || src.MessageType != "text" ||
		src.Extra["octo_message_id"] != originalID {
		return evidence, nil
	}
	question := src.Content
	if clean, ok := types.OctoAddressedQuestion(src.Extra); ok {
		question = clean
	}
	if !validText(question, 8000) {
		return evidence, nil
	}
	evidence.Question, evidence.QuestionMessageID = question, originalID
	if original.Reply != "" {
		evidence.Answer = original.Reply
	}
	return evidence, nil
}

func ignoreMissingEvidence(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

type evidenceInbox struct {
	Input, Authority, Reply, State string
}

type evidenceMessage struct {
	MessageType, UserID, UserName, Content string
	Extra                                  map[string]string
	Quote                                  *struct {
		MessageID, SenderID, Content string
		IsBotMessage                 bool
	}
}

func (r evidenceInbox) message(issue *Issue) *evidenceMessage {
	var input evidenceMessage
	var auth struct {
		AccountID, ScopeID string
		KnowledgeBaseIDs   []string
	}
	if json.Unmarshal([]byte(r.Input), &input) != nil || json.Unmarshal([]byte(r.Authority), &auth) != nil ||
		input.UserID != issue.ReporterUID || auth.AccountID != issue.AccountID || auth.ScopeID != issue.ScopeID ||
		!contains(auth.KnowledgeBaseIDs, issue.KnowledgeBaseID) ||
		input.Extra["octo_group_id"] != issue.GroupID || input.Extra["octo_subarea_id"] != issue.SubareaID {
		return nil
	}
	return &input
}

func (m evidenceMessage) originalQuestionID() string {
	id, user := m.originalQuestionAnchor()
	if user != m.UserID {
		return ""
	}
	return id
}

func (m evidenceMessage) originalQuestionAnchor() (string, string) {
	var payload struct {
		Reply struct {
			ID      json.RawMessage `json:"message_id"`
			Payload struct {
				Reply struct {
					ID   json.RawMessage `json:"message_id"`
					User string          `json:"from_uid"`
				} `json:"reply"`
			} `json:"payload"`
		} `json:"reply"`
	}
	if m.Quote == nil || json.Unmarshal([]byte(m.Extra["octo_original_payload"]), &payload) != nil ||
		decimalNativeID(payload.Reply.ID) != m.Quote.MessageID {
		return "", ""
	}
	return decimalNativeID(payload.Reply.Payload.Reply.ID), payload.Reply.Payload.Reply.User
}

func decimalNativeID(raw json.RawMessage) string {
	value := strings.Trim(string(raw), "\"")
	nonDecimal := strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0
	if value == "" || len(value) > 32 || nonDecimal {
		return ""
	}
	return value
}

type issueRetest struct {
	ID      string   `json:"id"`
	Answer  string   `json:"answer"`
	ModelID string   `json:"model_id"`
	Sources []string `json:"sources"`
}

var (
	errRetestUnavailable = errors.New("original question or authorized channel is unavailable for retest")
	errRetestIncomplete  = errors.New("retest did not produce a complete answer")
)

func (s *Service) retestAgent(ctx context.Context, issue *Issue) (*types.CustomAgent, error) {
	store := octointegration.NewStore(s.db)
	scope, err := store.Get(ctx, issue.TenantID, issue.ScopeID)
	if err != nil || scope.AccountID != issue.AccountID || scope.GroupID != issue.GroupID ||
		scope.SubareaID != issue.SubareaID || scope.VerifiedAt == nil ||
		scope.SyncStatus != "verified" || scope.NameSource != "octo" {
		return nil, errRetestUnavailable
	}
	bindings, err := store.Effective(ctx, issue.TenantID, scope.ID)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, binding := range bindings {
		allowed = allowed || binding.KnowledgeBaseID == issue.KnowledgeBaseID
	}
	if !allowed {
		return nil, errRetestUnavailable
	}
	var agent types.CustomAgent
	if err := s.db.WithContext(ctx).Table("custom_agents AS a").Select("a.*").
		Joins("JOIN im_channels AS c ON c.tenant_id = a.tenant_id AND c.agent_id = a.id").
		Where("a.tenant_id = ? AND a.deleted_at IS NULL AND c.id = ? AND c.enabled = ?",
			issue.TenantID, issue.ChannelID, true).
		Where("c.platform = 'octo' AND c.deleted_at IS NULL").
		Where("c.credentials->>'account_id' = ?", issue.AccountID).
		Take(&agent).Error; err != nil {
		return nil, errRetestUnavailable
	}
	if !agent.IsAgentMode() {
		return nil, errRetestUnavailable
	}
	return &agent, nil
}

func (s *Service) retestIssue(
	ctx context.Context, sessions interfaces.SessionService, id string,
) (*issueRetest, error) {
	p, issue, err := s.retestAdminIssue(ctx, id)
	if err != nil {
		return nil, err
	}
	evidence, err := s.issueEvidence(ctx, issue)
	if err != nil {
		return nil, err
	}
	if evidence == nil || evidence.Question == "" {
		return nil, errRetestUnavailable
	}
	agent, err := s.retestAgent(ctx, issue)
	if err != nil {
		return nil, err
	}
	run, err := s.beginIssueRetest(ctx, p, issue, evidence.QuestionMessageID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	result, executionErr := s.executeIssueRetest(ctx, sessions, issue, evidence, agent)
	if err = s.finishIssueRetest(ctx, issue, run, result, executionErr); err != nil {
		return nil, err
	}
	if executionErr != nil {
		return nil, executionErr
	}
	result.ID = run.ID
	return result, nil
}

func (s *Service) executeIssueRetest(
	ctx context.Context, sessions interfaces.SessionService, issue *Issue,
	evidence *issueEvidence, agent *types.CustomAgent,
) (*issueRetest, error) {
	// Reuse live model settings with an explicit read-only scope and a fresh
	// transient conversation. No message is sent and no role is impersonated.
	agent.Config.KBSelectionMode = "selected"
	agent.Config.KnowledgeBases = []string{issue.KnowledgeBaseID}
	configured := agent.Config.AllowedTools
	agent.Config.AllowedTools = []string{}
	for _, tool := range []string{"search_knowledge", "read_document", "wiki_search", "wiki_read_page"} {
		if (configured == nil || contains(configured, tool)) &&
			(tool != "search_knowledge" || agent.Config.RerankModelID != "") {
			agent.Config.AllowedTools = append(agent.Config.AllowedTools, tool)
		}
	}
	if len(agent.Config.AllowedTools) == 0 {
		return nil, errRetestUnavailable
	}
	agent.Config.AgentType = "custom"
	agent.Config.RetrieveKBOnlyWhenMentioned = false
	agent.Config.MultiTurnEnabled = false
	agent.Config.SandboxConfigID = ""
	agent.Config.DataAnalysisEnabled = false
	agent.Config.WebFetchEnabled = false
	agent.Config.WebSearchEnabled = false
	agent.Config.MCPSelectionMode = "none"
	agent.Config.SkillsSelectionMode = "none"
	ctx = WithRetrievalTrace(types.WithIMKnowledgeScope(ctx, agent.Config.KnowledgeBases))
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	bus := event.NewEventBus()
	result := &issueRetest{Sources: []string{}}
	var incomplete bool
	var wikiSlugs []string
	bus.On(event.EventError, func(_ context.Context, _ event.Event) error {
		incomplete = true
		return nil
	})
	bus.On(event.EventAgentFinalAnswer, func(_ context.Context, e event.Event) error {
		if data, ok := e.Data.(event.AgentFinalAnswerData); ok && (data.Truncated || data.IsFallback) {
			incomplete = true
		}
		return nil
	})
	bus.On(event.EventAgentComplete, func(_ context.Context, e event.Event) error {
		if data, ok := e.Data.(event.AgentCompleteData); ok {
			result.Answer, result.ModelID = data.FinalAnswer, data.ModelID
			for _, raw := range data.KnowledgeRefs {
				ref, ok := raw.(*types.SearchResult)
				if ok && ref != nil && ref.KnowledgeBaseID == issue.KnowledgeBaseID &&
					ref.KnowledgeTitle != "" && !contains(result.Sources, ref.KnowledgeTitle) {
					result.Sources = append(result.Sources, ref.KnowledgeTitle)
				}
			}
		}
		return nil
	})
	bus.On(event.EventAgentToolResult, func(_ context.Context, e event.Event) error {
		data, ok := e.Data.(event.AgentToolResultData)
		if !ok || !data.Success || data.ToolName != "wiki_read_page" {
			return nil
		}
		found, _ := data.Data["found_kbs"].(map[string][]string)
		omitted, _ := data.Data["omitted_slugs"].([]string)
		for slug, ids := range found {
			if len(wikiSlugs) < 50 && contains(ids, issue.KnowledgeBaseID) &&
				!contains(omitted, slug) && !contains(wikiSlugs, slug) {
				wikiSlugs = append(wikiSlugs, slug)
			}
		}
		return nil
	})
	request := &types.QARequest{
		Session:            &types.Session{ID: uuid.NewString(), TenantID: issue.TenantID},
		AssistantMessageID: uuid.NewString(), CustomAgent: agent, Query: evidence.Question,
		KnowledgeBaseIDs: agent.Config.KnowledgeBases, SharedAgentReadOnly: true,
	}
	if err := sessions.AgentQA(ctx, request, bus); err != nil || incomplete || strings.TrimSpace(result.Answer) == "" {
		return nil, errRetestIncomplete
	}
	if _, err := s.retestAgent(ctx, issue); err != nil {
		return nil, err
	}
	if len(wikiSlugs) > 0 {
		var titles []string
		if err := s.db.WithContext(ctx).Model(&types.WikiPage{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND status = ? AND slug IN ?",
				issue.TenantID, issue.KnowledgeBaseID, types.WikiPageStatusPublished, wikiSlugs).
			Order("title").Distinct("title").Pluck("title", &titles).Error; err != nil {
			return nil, err
		}
		for _, title := range titles {
			if !contains(result.Sources, title) {
				result.Sources = append(result.Sources, title)
			}
		}
	}
	return result, nil
}
