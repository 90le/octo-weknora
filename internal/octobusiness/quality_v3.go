package octobusiness

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type qualityFilterV3 struct {
	qualityFilter
	SourceType string // "" | "group" | "subarea" | "dm"
	ChannelID  string
	IncludeDM  bool
}

type convertQualityInput struct {
	ScopeID         string `json:"scope_id"`
	ChannelID       string `json:"channel_id"`
	IsDirect        bool   `json:"is_direct"`
	Title           string `json:"title"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Kind            string `json:"kind"` // missing | bug | suggestion
	Note            string `json:"note"`
	ContactID       string `json:"contact_id"`
}

// ConvertQualityToIssue converts a reviewed or abnormal Octo QA turn (group,
// subarea, or private DM) directly into a tracked Issue without requiring a
// redundant AI re-analysis step.
func (h *Handler) ConvertQualityToIssue(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var in convertQualityInput
	if c.ShouldBindJSON(&in) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	issue, err := h.service.convertQualityToIssue(ctx, c.Param("id"), in)
	respond(c, issue, err)
}

// RetryFailedQuality requeues failed AI background quality reviews for the tenant.
func (h *Handler) RetryFailedQuality(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var in struct {
		ScopeID   string `json:"scope_id"`
		MessageID string `json:"message_id"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 && c.ShouldBindJSON(&in) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	count, err := h.service.retryFailedQualityReviews(ctx, in.ScopeID, in.MessageID)
	respond(c, gin.H{"retried": count}, err)
}

func (s *Service) retryFailedQualityReviews(ctx context.Context, scopeID, messageID string) (int64, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return 0, ErrDenied
	}
	q := s.db.WithContext(ctx).Model(&qualityReview{}).
		Where("tenant_id = ? AND state = 'failed'", p.TenantID)
	if scopeID != "" {
		q = q.Where("scope_id = ?", scopeID)
	}
	if messageID != "" {
		q = q.Where("message_id = ?", messageID)
	}
	res := q.Updates(map[string]any{
		"state":         "queued",
		"attempt_count": 0,
		"available_at":  nil,
		"updated_at":    time.Now().UTC(),
	})
	return res.RowsAffected, res.Error
}

func (s *Service) convertQualityToIssue(ctx context.Context, messageID string, in convertQualityInput) (*Issue, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if !validText(messageID, 255) || !validText(in.Title, 300) || !validText(in.KnowledgeBaseID, 128) || len(in.Note) > 4000 {
		return nil, ErrInvalid
	}
	if _, err := s.checkKB(ctx, p, in.KnowledgeBaseID, false); err != nil {
		return nil, err
	}
	kind := in.Kind
	if !contains([]string{"missing", "bug", "suggestion"}, kind) {
		kind = "suggestion"
	}

	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	var entry struct {
		ChannelID, MessageID, Input, Authority, Reply, State string
		CreatedAt                                            time.Time
	}
	q := quiet.Table("octo_message_inbox").
		Select("channel_id, message_id, input, authority, reply, state, created_at").
		Where("tenant_id = ? AND message_id = ?", p.TenantID, messageID)
	if in.ChannelID != "" {
		q = q.Where("channel_id = ?", in.ChannelID)
	}
	if err := q.Take(&entry).Error; err != nil {
		return nil, err
	}

	var input struct {
		MessageType, UserID, UserName, Content, ChatType string
		Extra                                            map[string]string
	}
	var authority struct {
		AccountID, ScopeID string
		KnowledgeBaseIDs   []string
	}
	if json.Unmarshal([]byte(entry.Input), &input) != nil || json.Unmarshal([]byte(entry.Authority), &authority) != nil {
		return nil, ErrInvalid
	}
	question := strings.TrimSpace(input.Content)
	if clean, ok := types.OctoAddressedQuestion(input.Extra); ok {
		question = strings.TrimSpace(clean)
	}
	isDirect := input.ChatType == "direct" || in.IsDirect
	scopeID := authority.ScopeID
	if scopeID == "" {
		scopeID = in.ScopeID
	}
	scopeName := "Octo 私聊"
	groupID, subareaID := input.Extra["octo_group_id"], input.Extra["octo_subarea_id"]
	if !isDirect && scopeID != "" {
		if sc, scErr := octointegration.NewStore(s.db).Get(ctx, p.TenantID, scopeID); scErr == nil {
			scopeName = sc.DisplayName
			groupID, subareaID = sc.GroupID, sc.SubareaID
		}
	}
	reporterName := strings.TrimSpace(input.UserName)
	if input.UserID != "" && (reporterName == "" || reporterName == input.UserID) {
		if resolved := s.resolveOctoNicknames(ctx, p.TenantID, entry.ChannelID, []string{input.UserID}); resolved[input.UserID] != "" {
			reporterName = resolved[input.UserID]
		}
	}
	if isDirect {
		scopeName = "私聊 · " + firstNonEmpty(reporterName, input.UserID)
	}

	desc := strings.TrimSpace(in.Note)
	var existingReview qualityReview
	hasReview := quiet.Where("tenant_id = ? AND channel_id = ? AND message_id = ?", p.TenantID, entry.ChannelID, messageID).
		Limit(1).Find(&existingReview).RowsAffected > 0
	if hasReview && existingReview.Finding != "" {
		var finding qualityFinding
		if json.Unmarshal([]byte(existingReview.Finding), &finding) == nil {
			parts := []string{}
			if desc != "" {
				parts = append(parts, desc)
			}
			if finding.Reason != "" {
				parts = append(parts, "AI 复核说明："+finding.Reason)
			}
			if finding.Suggestion != "" {
				parts = append(parts, "改进建议："+finding.Suggestion)
			}
			desc = strings.Join(parts, "\n")
		}
	}
	if desc == "" {
		desc = "从 Octo 问答质量工作台一键转入跟进。"
	}

	issue := &Issue{
		TenantID:        p.TenantID,
		AccountID:       authority.AccountID,
		ChannelID:       entry.ChannelID,
		ScopeID:         scopeID,
		ScopeName:       scopeName,
		GroupID:         groupID,
		SubareaID:       subareaID,
		IsDirect:        isDirect,
		KnowledgeBaseID: in.KnowledgeBaseID,
		Kind:            kind,
		Title:           strings.TrimSpace(in.Title),
		Description:     limitQualityText(desc, 3900),
		Status:          "open",
		ReporterUID:     input.UserID,
		ReporterName:    firstNonEmpty(reporterName, input.UserID),
		MessageID:       entry.MessageID,
		OriginalMessage: limitQualityText(question, 4000),
		Attachments:     types.JSON("[]"),
	}
	issue.IdempotencyKey = qualityIssueKey(issue)

	if in.ContactID != "" {
		var contact Contact
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?",
			p.TenantID, issue.KnowledgeBaseID, in.ContactID).First(&contact).Error; err != nil {
			return nil, err
		}
		issue.OwnerUID, issue.OwnerName = contact.UID, contact.Name
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		author := p
		author.AccountID, author.ChannelID, author.ScopeID = issue.AccountID, issue.ChannelID, issue.ScopeID
		author.ScopeName, author.GroupID, author.SubareaID = issue.ScopeName, issue.GroupID, issue.SubareaID
		author.IsDirect, author.MessageID = issue.IsDirect, issue.MessageID
		if err := insertIssue(tx, author, issue); err != nil {
			return err
		}
		ev := IssueEvent{
			ID:             uuid.NewString(),
			TenantID:       p.TenantID,
			IssueID:        issue.ID,
			ActorUID:       p.UserID,
			ActorName:      p.UserName,
			Status:         issue.Status,
			Note:           "从问答质量一键登记为问题",
			IdempotencyKey: digest("quality_convert_v3", issue.ID),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ev).Error; err != nil {
			return err
		}
		scoped := *s
		scoped.db = tx
		review := qualityReview{
			TenantID:  p.TenantID,
			ChannelID: entry.ChannelID,
			ScopeID:   scopeID,
			MessageID: messageID,
			IssueID:   issue.ID,
			UpdatedAt: time.Now().UTC(),
		}
		if err := scoped.writeQualityReview(ctx, review, []string{"issue_id", "updated_at"}); err != nil {
			return err
		}
		return audit(tx, p, "octo.quality.convert_issue", issue.ID, map[string]string{
			"message_id": messageID,
			"is_direct":  boolStr(isDirect),
		})
	})
	return issue, err
}

// queryQualityV3 supports cross-scope aggregation ("all authorized scopes"),
// private DM inclusion with strict group-key isolation, interrupted turns,
// and sanitized Agent tool-trace enrichment while staying compatible with
// single-scope queries.
func (s *Service) queryQualityV3(
	ctx context.Context, scopeID string, f qualityFilterV3,
) (*qualityPage, error) {
	if scopeID != "" && f.SourceType == "" && !f.IncludeDM && f.ChannelID == "" {
		return s.queryQuality(ctx, scopeID, f.qualityFilter)
	}
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.AddDate(0, 0, -14)
	}
	if !f.From.Before(f.To) || f.To.After(f.From.AddDate(0, 0, 93)) || len(f.Keyword) > 600 || f.Page > 10000 {
		return nil, ErrInvalid
	}
	f.Page = max(1, f.Page)
	if f.PageSize <= 0 {
		f.PageSize = 20
	}
	f.PageSize = min(f.PageSize, 100)

	// Load all verified scopes and tenant KBs for authorization & display.
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	var scopes []octointegration.Scope
	_ = quiet.Where("tenant_id = ? AND sync_status = 'verified' AND verified_at IS NOT NULL", p.TenantID).Find(&scopes).Error
	scopeByID := map[string]*octointegration.Scope{}
	for i := range scopes {
		scopeByID[scopes[i].ID] = &scopes[i]
	}

	var kbs []struct{ ID, Name string }
	_ = quiet.Table("knowledge_bases").Select("id, name").Where("tenant_id = ? AND deleted_at IS NULL", p.TenantID).Order("name, id").Find(&kbs).Error
	libraries := make([]qualityLibrary, 0, len(kbs))
	validKBs := map[string]bool{}
	for _, kb := range kbs {
		if strings.HasPrefix(kb.Name, "__") {
			continue
		}
		validKBs[kb.ID] = true
		libraries = append(libraries, qualityLibrary{ID: kb.ID, Name: kb.Name})
	}

	botNames := map[string]string{}
	var channels []struct{ ID, Name string }
	if quiet.Table("im_channels").Select("id, name").Where("tenant_id = ? AND platform = 'octo' AND deleted_at IS NULL", p.TenantID).Find(&channels).Error == nil {
		for _, ch := range channels {
			botNames[ch.ID] = ch.Name
		}
	}

	q := quiet.Table("octo_message_inbox").
		Select("channel_id, message_id, input, authority, reply, state, created_at").
		Where("tenant_id = ? AND state IN ? AND created_at >= ? AND created_at < ?",
			p.TenantID, []string{"delivered", "failed"}, f.From, f.To)
	if f.ChannelID != "" {
		q = q.Where("channel_id = ?", f.ChannelID)
	}
	if scopeID != "" {
		q = q.Where("authority LIKE ?", `%"ScopeID":"`+scopeID+`"%`)
	}
	cursor, err := q.Order("created_at DESC, message_id DESC").Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = cursor.Close() }()

	const candidateLimit = 12000
	rows := []qualityRow{}
	truncated := false
	for count := 0; cursor.Next(); count++ {
		if count == candidateLimit {
			truncated = true
			break
		}
		var entry struct {
			ChannelID, MessageID, Input, Authority, Reply, State string
			CreatedAt                                            time.Time
		}
		if err := s.db.ScanRows(cursor, &entry); err != nil {
			return nil, err
		}
		row, ok := parseQualityInboxV3(entry.ChannelID, entry.MessageID, entry.Input, entry.Authority, entry.Reply, entry.State, entry.CreatedAt, scopeByID, validKBs, botNames)
		if !ok {
			continue
		}
		if scopeID != "" && row.ScopeID != scopeID {
			continue
		}
		if f.SourceType == "dm" && row.SourceType != "dm" {
			continue
		}
		if f.SourceType == "group" && row.SourceType != "group" {
			continue
		}
		if f.SourceType == "subarea" && row.SourceType != "subarea" {
			continue
		}
		if !f.IncludeDM && row.SourceType == "dm" {
			continue
		}
		if f.LibraryID != "" && !contains(row.libraries, f.LibraryID) {
			continue
		}
		rows = append(rows, row)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}

	s.enrichQualityV3(ctx, p.TenantID, rows)

	counts := map[string]int{}
	for _, row := range rows {
		if row.Outcome == "suspected_gap" {
			counts[row.GroupKey]++
		}
	}
	for i := range rows {
		rows[i].Repeats = counts[rows[i].GroupKey]
	}

	keyword := strings.ToLower(strings.TrimSpace(f.Keyword))
	// When filtering by keyword, pre-resolve Octo nicknames (TTL-cached) so operators
	// can search directly by member / DM user nickname.
	if keyword != "" && len(rows) > 0 && s.ResolveOctoUserNames != nil {
		byChannel := map[string][]string{}
		for _, r := range rows {
			if r.userID != "" && (r.AskerName == "" || r.AskerName == r.userID) {
				byChannel[r.ChannelID] = append(byChannel[r.ChannelID], r.userID)
			}
		}
		resolved := map[string]string{}
		for chID, uids := range byChannel {
			for uid, nick := range s.resolveOctoNicknames(ctx, p.TenantID, chID, uids) {
				if nick != "" {
					resolved[uid] = nick
				}
			}
		}
		for i := range rows {
			if nick := resolved[rows[i].userID]; nick != "" {
				rows[i].AskerName = nick
				if rows[i].SourceType == "dm" {
					rows[i].ScopeName = "私聊 · " + nick
				}
			}
		}
	}
	filtered := make([]qualityRow, 0, len(rows))
	for _, row := range rows {
		if !qualityMatchesV3(row, f.Outcome) ||
			(f.Group != "" && f.Group != row.GroupKey) ||
			(f.Topic != "" && (row.Review == nil || row.Review.Topic != f.Topic)) ||
			(keyword != "" && !strings.Contains(strings.ToLower(row.Question+"\n"+row.Answer+"\n"+row.ScopeName+"\n"+row.AskerName+"\n"+row.userID), keyword)) {
			continue
		}
		filtered = append(filtered, row)
	}

	page := &qualityPage{
		Rows:       []qualityRow{},
		Libraries:  libraries,
		SampleSize: len(rows),
		Truncated:  truncated,
		Since:      f.From,
		Until:      f.To,
		Total:      len(filtered),
		Stats:      qualityStats(filtered),
	}
	start := min(len(filtered), (f.Page-1)*f.PageSize)
	page.Rows = append(page.Rows, filtered[start:min(start+f.PageSize, len(filtered))]...)
	// Resolve Octo user nicknames for the current page (bounded & TTL-cached).
	if len(page.Rows) > 0 && s.ResolveOctoUserNames != nil {
		byChannel := map[string][]string{}
		for _, r := range page.Rows {
			if r.userID != "" && (r.AskerName == "" || r.AskerName == r.userID) {
				byChannel[r.ChannelID] = append(byChannel[r.ChannelID], r.userID)
			}
		}
		resolved := map[string]string{}
		for chID, uids := range byChannel {
			for uid, nick := range s.resolveOctoNicknames(ctx, p.TenantID, chID, uids) {
				if nick != "" {
					resolved[uid] = nick
				}
			}
		}
		for i := range page.Rows {
			if nick := resolved[page.Rows[i].userID]; nick != "" {
				page.Rows[i].AskerName = nick
				if page.Rows[i].SourceType == "dm" {
					page.Rows[i].ScopeName = "私聊 · " + nick
				}
			}
		}
	}
	return page, nil
}

func parseQualityInboxV3(
	channelID, messageID, rawInput, rawAuthority, reply, state string,
	createdAt time.Time,
	scopeByID map[string]*octointegration.Scope,
	validKBs map[string]bool,
	botNames map[string]string,
) (qualityRow, bool) {
	var input struct {
		MessageType, UserID, UserName, Content, ChatType string
		Extra                                            map[string]string
	}
	var authority struct {
		AccountID, ScopeID string
		KnowledgeBaseIDs   []string
	}
	if json.Unmarshal([]byte(rawInput), &input) != nil ||
		json.Unmarshal([]byte(rawAuthority), &authority) != nil ||
		input.MessageType != "text" || input.UserID == "" ||
		input.Extra["octo_card_event_key"] != "" {
		return qualityRow{}, false
	}
	question := input.Content
	if clean, ok := types.OctoAddressedQuestion(input.Extra); ok {
		question = clean
	}
	question = strings.TrimSpace(question)
	if _, _, cmd := Confirmation(question); cmd || strings.HasPrefix(question, "/") || question == "" {
		return qualityRow{}, false
	}

	isDirect := input.ChatType == "direct" || authority.ScopeID == ""
	srcType := "group"
	scopeName := ""
	if isDirect {
		srcType = "dm"
		scopeName = "私聊 · " + firstNonEmpty(input.UserName, input.UserID)
	} else {
		sc := scopeByID[authority.ScopeID]
		if sc == nil {
			return qualityRow{}, false
		}
		scopeName = sc.DisplayName
		if sc.SubareaID != "" {
			srcType = "subarea"
		}
	}

	libs := make([]string, 0, len(authority.KnowledgeBaseIDs))
	for _, kb := range authority.KnowledgeBaseIDs {
		if len(validKBs) == 0 || validKBs[kb] {
			libs = append(libs, kb)
		}
	}
	evidence := QualityTurn{}
	_ = json.Unmarshal([]byte(input.Extra[QualityTurnKey]), &evidence)
	inbox := qualityInbox{MessageID: messageID, Input: rawInput, Authority: rawAuthority, Reply: reply, State: state, CreatedAt: createdAt}
	row := qualityRow{
		ID:          messageID,
		Question:    limitQualityText(question, 4000),
		Answer:      limitQualityText(reply, 8000),
		Outcome:     qualityOutcome(inbox, evidence),
		CreatedAt:   createdAt,
		SourceType:  srcType,
		ScopeID:     authority.ScopeID,
		ScopeName:   scopeName,
		ChannelID:   channelID,
		BotName:     botNames[channelID],
		AskerName:   firstNonEmpty(input.UserName, input.UserID),
		Interrupted: state != "delivered",
		userID:      input.UserID,
		name:        input.UserName,
		libraries:   libs,
	}
	if state != "delivered" {
		row.Answer = ""
	}
	for _, src := range evidence.Sources {
		if (len(libs) == 0 || contains(libs, src.Library)) && (src.WikiSlug != "" || src.ChunkID != "") && len(row.Sources) < 12 {
			row.Sources = append(row.Sources, src)
		}
	}
	sortedLibs := append([]string(nil), libs...)
	sort.Strings(sortedLibs)
	if isDirect {
		// Isolate private DM question groups strictly from group scopes.
		row.GroupKey = "dm:" + digest("dm", channelID, input.UserID, qualityQuestionKey(row.Question), strings.Join(sortedLibs, ","))
	} else {
		row.GroupKey = digest(authority.ScopeID, channelID, qualityQuestionKey(row.Question), strings.Join(sortedLibs, ","))
	}
	return row, true
}

func (s *Service) enrichQualityV3(ctx context.Context, tenant uint64, rows []qualityRow) {
	if len(rows) == 0 {
		return
	}
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	for i := 0; i < len(rows); i += 80 {
		window := rows[i:min(i+80, len(rows))]
		ids := make([]string, 0, len(window))
		for _, r := range window {
			ids = append(ids, r.ID)
		}
		var reviews []qualityReview
		if quiet.Model(&qualityReview{}).Where("tenant_id = ? AND message_id IN ?", tenant, ids).Find(&reviews).Error == nil {
			byMsg := map[string]qualityReview{}
			for _, rv := range reviews {
				byMsg[rv.MessageID] = rv
			}
			for j := range window {
				if rv, ok := byMsg[window[j].ID]; ok {
					view := rv.view()
					window[j].Review = &view
					if rv.GroupID != "" && window[j].SourceType != "dm" {
						window[j].GroupKey = rv.GroupID
					}
				}
			}
		}
		// Best-effort enrichment from assistant messages (is_completed + sanitized agent_steps).
		var msgs []struct {
			RequestID   string
			IsCompleted bool
			AgentSteps  string
		}
		if quiet.Table("messages").Select("request_id, is_completed, agent_steps").
			Where("role = 'assistant' AND request_id IN ?", ids).Find(&msgs).Error == nil {
			byReq := map[string]struct {
				done  bool
				steps string
			}{}
			for _, m := range msgs {
				byReq[m.RequestID] = struct {
					done  bool
					steps string
				}{m.IsCompleted, m.AgentSteps}
			}
			for j := range window {
				if meta, ok := byReq[window[j].ID]; ok {
					if !meta.done {
						window[j].Interrupted = true
						if window[j].Outcome == "answered" {
							window[j].Outcome = "failed"
						}
					}
					window[j].AgentTrace = parseAgentTraceSummary(meta.steps)
				}
			}
		}
		_ = s.markUnavailableQualitySources(ctx, tenant, window)
	}
}

func qualityMatchesV3(row qualityRow, outcome string) bool {
	if outcome == "abnormal" {
		return row.Interrupted || row.Outcome == "failed" || row.Outcome == "retrieval_warning" ||
			(row.Review != nil && row.Review.State == "failed")
	}
	if outcome == "done" {
		return row.Review != nil && (row.Review.Disposition != "" || row.Review.IssueID != "")
	}
	return qualityMatches(row, outcome)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
