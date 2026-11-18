package octobusiness

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type qualityScope struct {
	*octointegration.Scope
	ChannelID string
	ModelID   string
	Libraries []qualityLibrary
}

type qualityLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (q *qualityScope) permits(id string) bool {
	for _, library := range q.Libraries {
		if library.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) qualityScope(ctx context.Context, scopeID string) (*qualityScope, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console || !validText(scopeID, 128) {
		return nil, ErrDenied
	}
	store := octointegration.NewStore(s.db)
	scope, err := store.Get(ctx, p.TenantID, scopeID)
	if err != nil {
		return nil, err
	}
	if scope.VerifiedAt == nil || scope.SyncStatus != "verified" || scope.NameSource != "octo" {
		return nil, ErrDenied
	}
	bindings, err := store.Effective(ctx, p.TenantID, scopeID)
	if err != nil {
		return nil, err
	}
	out := &qualityScope{Scope: scope, Libraries: []qualityLibrary{}}
	for _, binding := range bindings {
		kb, err := s.checkKB(ctx, p, binding.KnowledgeBaseID, false)
		if err != nil {
			return nil, err
		}
		out.Libraries = append(out.Libraries, qualityLibrary{ID: kb.ID, Name: kb.Name})
	}
	var channel struct {
		ID, ModelID string
	}
	err = s.db.WithContext(ctx).Table("im_channels AS c").
		Select("c.id, a.config->>'model_id' AS model_id").
		Joins("JOIN custom_agents a ON a.id = c.agent_id AND a.tenant_id = c.tenant_id AND a.deleted_at IS NULL").
		Where("c.tenant_id = ? AND c.platform = 'octo' AND c.deleted_at IS NULL", p.TenantID).
		Where("c.credentials->>'account_id' = ?", scope.AccountID).Order("c.enabled DESC, c.id").Take(&channel).Error
	if err != nil {
		return nil, err
	}
	out.ChannelID, out.ModelID = channel.ID, channel.ModelID
	return out, nil
}

type qualityRow struct {
	ID                 string          `json:"id"`
	Question           string          `json:"question"`
	Answer             string          `json:"answer"`
	Outcome            string          `json:"outcome"`
	CreatedAt          time.Time       `json:"created_at"`
	Repeats            int             `json:"unanswered_repeats"`
	GroupKey           string          `json:"group_key"`
	Sources            []QualitySource `json:"sources,omitempty"`
	UnavailableSources int             `json:"unavailable_sources"`
	userID             string
	name               string
	libraries          []string
}

type qualityPage struct {
	Rows       []qualityRow     `json:"rows"`
	Libraries  []qualityLibrary `json:"libraries"`
	SampleSize int              `json:"sample_size"`
	Truncated  bool             `json:"truncated"`
	Since      time.Time        `json:"since"`
}

type qualityInbox struct {
	MessageID, Input, Authority, Reply, State string
	CreatedAt                                 time.Time
}

func (entry qualityInbox) row(scope *qualityScope) (qualityRow, bool) {
	var input struct {
		MessageType, UserID, UserName, Content, ChatType string
		Extra                                            map[string]string
	}
	var authority struct {
		AccountID, ScopeID                       string
		KnowledgeBaseIDs, ManageKnowledgeBaseIDs []string
	}
	if json.Unmarshal([]byte(entry.Input), &input) != nil ||
		json.Unmarshal([]byte(entry.Authority), &authority) != nil ||
		input.MessageType != "text" || input.ChatType != "group" || input.UserID == "" ||
		input.Extra["octo_group_id"] != scope.GroupID || input.Extra["octo_subarea_id"] != scope.SubareaID ||
		input.Extra["octo_card_event_key"] != "" || authority.ScopeID != scope.ID ||
		authority.AccountID != scope.AccountID || len(authority.KnowledgeBaseIDs) == 0 {
		return qualityRow{}, false
	}
	for _, library := range append(authority.KnowledgeBaseIDs, authority.ManageKnowledgeBaseIDs...) {
		if !scope.permits(library) {
			return qualityRow{}, false
		}
	}
	question := input.Content
	if clean, ok := types.OctoAddressedQuestion(input.Extra); ok {
		question = clean
	}
	question = strings.TrimSpace(question)
	if _, _, command := Confirmation(question); command || strings.HasPrefix(question, "/") || question == "" {
		return qualityRow{}, false
	}
	evidence := QualityTurn{}
	_ = json.Unmarshal([]byte(input.Extra[QualityTurnKey]), &evidence)
	row := qualityRow{
		ID: entry.MessageID, Question: limitQualityText(question, 4000), Answer: limitQualityText(entry.Reply, 8000),
		Outcome: qualityOutcome(entry, evidence), CreatedAt: entry.CreatedAt,
		userID: input.UserID, name: input.UserName,
		libraries: authority.KnowledgeBaseIDs,
	}
	if entry.State != "delivered" {
		row.Answer = ""
	}
	for _, source := range evidence.Sources {
		if contains(row.libraries, source.Library) && (source.WikiSlug != "" || source.ChunkID != "") &&
			len(row.Sources) < 12 {
			row.Sources = append(row.Sources, source)
		}
	}
	return row, true
}

func qualityOutcome(entry qualityInbox, evidence QualityTurn) string {
	if entry.State != "delivered" || evidence.Status == "failed" {
		return "failed"
	}
	if evidence.Status == "retrieval_warning" {
		return "retrieval_warning"
	}
	lead, _, _ := strings.Cut(strings.TrimSpace(entry.Reply), "\n\n")
	lead = strings.ToLower(limitQualityText(lead, 350))
	for _, phrase := range []string{
		"没有答案", "未找到相关", "没有找到相关", "资料中没有", "没有可检索", "缺少依据", "无法核实",
		"未说明", "未提供", "没有写明", "没有明确", "无法确认", "没有提到", "未提及", "我无法给出",
		"not enough information", "could not find",
	} {
		if strings.Contains(lead, phrase) {
			return "suspected_gap"
		}
	}
	return "answered"
}

func limitQualityText(value string, size int) string {
	runes := []rune(value)
	if len(runes) > size {
		return string(runes[:size]) + "…"
	}
	return value
}

func qualityQuestionKey(question string) string {
	question = strings.TrimRight(strings.TrimSpace(question), "?？。!！")
	return strings.ToLower(strings.Join(strings.Fields(question), " "))
}

func (s *Service) qualityInbox(ctx context.Context, scope *qualityScope) *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).
		Table("octo_message_inbox").Where("tenant_id = ? AND channel_id = ?", scope.TenantID, scope.ChannelID).
		Where("state IN ?", []string{"delivered", "failed"})
}

const qualitySampleLimit = 250

func (s *Service) listQuality(ctx context.Context, scopeID string) (*qualityPage, error) {
	scope, err := s.qualityScope(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	page := &qualityPage{Rows: []qualityRow{}, Libraries: scope.Libraries, Since: time.Now().AddDate(0, 0, -14)}
	var entries []qualityInbox
	// The LIKE clause narrows candidates. Structured authority is checked again before exposing any payload.
	pattern := `%"ScopeID":"` + scope.ID + `"%`
	err = s.qualityInbox(ctx, scope).
		Where("created_at >= ? AND authority LIKE ?", page.Since, pattern).
		Order("created_at DESC, message_id DESC").Limit(qualitySampleLimit + 1).Find(&entries).Error
	if err != nil {
		return nil, err
	}
	page.Truncated = len(entries) > qualitySampleLimit
	counts := map[string]int{}
	for _, entry := range entries[:min(len(entries), qualitySampleLimit)] {
		if row, ok := entry.row(scope); ok {
			page.Rows = append(page.Rows, row)
			if row.Outcome == "suspected_gap" {
				counts[qualityQuestionKey(row.Question)]++
			}
		}
	}
	for i := range page.Rows {
		key := qualityQuestionKey(page.Rows[i].Question)
		page.Rows[i].Repeats = counts[key]
		page.Rows[i].GroupKey = digest("quality-question", key)
	}
	if err := s.markUnavailableQualitySources(ctx, scope.TenantID, page.Rows); err != nil {
		return nil, err
	}
	page.SampleSize = len(page.Rows)
	return page, nil
}

func (s *Service) markUnavailableQualitySources(ctx context.Context, tenant uint64, rows []qualityRow) error {
	seen := map[QualitySource]bool{}
	var wikiKeys [][]string
	var chunkIDs []string
	for _, row := range rows {
		for _, src := range row.Sources {
			if seen[src] {
				continue
			}
			seen[src] = true
			if src.WikiSlug != "" {
				wikiKeys = append(wikiKeys, []string{src.Library, src.WikiSlug})
			} else {
				chunkIDs = append(chunkIDs, src.ChunkID)
			}
		}
	}
	available := map[QualitySource]bool{}
	if len(wikiKeys) > 0 {
		var pages []struct{ KnowledgeBaseID, Slug string }
		err := s.db.WithContext(ctx).Model(&types.WikiPage{}).Select("knowledge_base_id, slug").
			Where("tenant_id = ? AND status = ?", tenant, types.WikiPageStatusPublished).
			Where("(knowledge_base_id, slug) IN ?", wikiKeys).Find(&pages).Error
		if err != nil {
			return err
		}
		for _, page := range pages {
			available[QualitySource{Library: page.KnowledgeBaseID, WikiSlug: page.Slug}] = true
		}
	}
	if len(chunkIDs) > 0 {
		var chunks []struct{ KnowledgeBaseID, ID string }
		err := s.readableQualityChunks(ctx, tenant).Select("c.knowledge_base_id, c.id").
			Where("c.id IN ?", chunkIDs).Find(&chunks).Error
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			available[QualitySource{Library: chunk.KnowledgeBaseID, ChunkID: chunk.ID}] = true
		}
	}
	for i := range rows {
		for _, source := range rows[i].Sources {
			if !available[source] {
				rows[i].UnavailableSources++
			}
		}
	}
	return nil
}
