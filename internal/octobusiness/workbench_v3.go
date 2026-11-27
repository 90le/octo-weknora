package octobusiness

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type workbenchAlerts struct {
	InterruptedCount int               `json:"interrupted_count"`
	FailedAIReviews  int               `json:"failed_ai_reviews"`
	PartialSyncKBs   []kbHealthSummary `json:"partial_sync_kbs"`
}

type workbenchKPIs struct {
	AIProcessCount   int     `json:"ai_process_count"`
	AIConvertedCount int     `json:"ai_converted_count"`
	OpenIssues       int     `json:"open_issues"`
	RepliedIssues    int     `json:"replied_issues"`
	InterruptedCount int     `json:"interrupted_count"`
	FailedAIReviews  int     `json:"failed_ai_reviews"`
	InterruptedRate  float64 `json:"interrupted_rate"`
	KBCount          int     `json:"kb_count"`
	WikiPages        int     `json:"wiki_pages"`
	PPTXCount        int     `json:"pptx_count"`
	XLSXCount        int     `json:"xlsx_count"`
}

type workbenchActionItem struct {
	Kind       string `json:"kind"` // qa | issue
	Priority   int    `json:"priority"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Badge      string `json:"badge"`
	BadgeTone  string `json:"badge_tone"` // err | warn | brand | ok
	SourceType string `json:"source_type"`
	Place      string `json:"place"`
	ScopeID    string `json:"scope_id"`
	ChannelID  string `json:"channel_id"`
	KBID       string `json:"knowledge_base_id"`
	KBName     string `json:"knowledge_base_name"`
	MetaTop    string `json:"meta_top"`
	MetaSub    string `json:"meta_sub"`
	SubText    string `json:"sub_text"`
	CTA        string `json:"cta"`
	Primary    bool   `json:"primary"`
}

type kbHealthSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Docs           int    `json:"docs"`
	Ready          int    `json:"ready"`
	Disabled       int    `json:"disabled"`
	PPTX           int    `json:"pptx"`
	XLSX           int    `json:"xlsx"`
	WikiPages      int    `json:"wiki_pages"`
	SyncStatus     string `json:"sync_status"` // ok | partial | failed | none
	PartialRuns14d int    `json:"partial_runs_14d"`
	LastSyncAt     string `json:"last_sync_at"`
	Schedule       string `json:"schedule"`
	SyncNote       string `json:"sync_note"`
}

type workbenchHotQuestion struct {
	ID         string `json:"id"`
	Question   string `json:"question"`
	KBName     string `json:"knowledge_base_name"`
	SourceType string `json:"source_type"`
	Count      int    `json:"count"`
	Users      int    `json:"users"`
}

type workbenchSummary struct {
	Since        time.Time              `json:"since"`
	Until        time.Time              `json:"until"`
	Alerts       workbenchAlerts        `json:"alerts"`
	KPIs         workbenchKPIs          `json:"kpis"`
	ActionStream []workbenchActionItem  `json:"action_stream"`
	KBHealth     []kbHealthSummary      `json:"kb_health"`
	HotQuestions []workbenchHotQuestion `json:"hot_questions"`
	PendingQA    int                    `json:"pending_qa"`
	OpenIssues   int                    `json:"open_issues"`
}

// WorkbenchSummary aggregates multi-signal alerts, KPIs, unified action stream,
// KB sync/Wiki health matrix, and top asks for the v3 Octo Operations Workbench.
func (h *Handler) WorkbenchSummary(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	filter := qualityFilterV3{
		qualityFilter: qualityFilter{
			LibraryID: c.Query("knowledge_base_id"),
			Keyword:   c.Query("keyword"),
		},
		SourceType: c.Query("source_type"),
		ChannelID:  c.Query("channel_id"),
		IncludeDM:  true,
	}
	for key, dst := range map[string]*time.Time{"from": &filter.From, "to": &filter.To} {
		if raw := c.Query(key); raw != "" {
			if *dst, err = time.Parse(time.RFC3339, raw); err != nil {
				respond(c, nil, ErrInvalid)
				return
			}
		}
	}
	out, err := h.service.workbenchSummary(ctx, c.Query("scope_id"), filter)
	respond(c, out, err)
}

func (s *Service) workbenchSummary(
	ctx context.Context, scopeID string, f qualityFilterV3,
) (*workbenchSummary, error) {
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
	f.Page, f.PageSize = 1, 100
	f.Outcome = "all"

	qPage, err := s.queryQualityV3(ctx, scopeID, f)
	if err != nil {
		return nil, err
	}

	issueFilter := IssueFilter{
		ScopeID:         scopeID,
		KnowledgeBaseID: f.LibraryID,
		Keyword:         f.Keyword,
		Visibility:      "active",
		Page:            1,
		PageSize:        50,
	}
	issuePage, err := s.ListIssues(ctx, issueFilter)
	if err != nil {
		return nil, err
	}

	kbNames := map[string]string{}
	for _, lib := range qPage.Libraries {
		kbNames[lib.ID] = lib.Name
	}
	kbHealth := s.collectKBHealth(ctx, p.TenantID, f.LibraryID)
	for _, kb := range kbHealth {
		kbNames[kb.ID] = kb.Name
	}

	replyCounts := s.issueReplyCounts(ctx, p.TenantID, issuePage.Items)
	scopeNames := s.scopeDisplayMap(ctx, p.TenantID)

	summary := &workbenchSummary{
		Since: f.From,
		Until: f.To,
		Alerts: workbenchAlerts{
			PartialSyncKBs: []kbHealthSummary{},
		},
		ActionStream: []workbenchActionItem{},
		KBHealth:     kbHealth,
		HotQuestions: []workbenchHotQuestion{},
	}

	for _, kb := range kbHealth {
		summary.KPIs.KBCount++
		summary.KPIs.WikiPages += kb.WikiPages
		summary.KPIs.PPTXCount += kb.PPTX
		summary.KPIs.XLSXCount += kb.XLSX
		if kb.SyncStatus == "partial" || kb.SyncStatus == "failed" {
			summary.Alerts.PartialSyncKBs = append(summary.Alerts.PartialSyncKBs, kb)
		}
	}

	var totalTurns, pendingQA, aiProc, aiConv, interrupted, failedAI int
	for _, row := range qPage.Rows {
		totalTurns++
		isDone := row.Review != nil && (row.Review.Disposition != "" || row.Review.IssueID != "")
		if row.Review != nil && row.Review.IssueID != "" {
			aiConv++
		}
		if row.Review != nil && row.Review.State == "failed" {
			failedAI++
		}
		if row.Interrupted || row.Outcome == "failed" {
			interrupted++
		}
		if !isDone && (row.Interrupted || row.Outcome != "answered" || (row.Review != nil && (row.Review.Suggestion == "process" || row.Review.State == "failed"))) {
			pendingQA++
		}
		if !isDone && (row.Interrupted || (row.Review != nil && row.Review.Suggestion == "process")) {
			aiProc++
			badge, tone, prio := "AI·建议处理", "warn", 2
			if row.Interrupted || row.Outcome == "failed" {
				badge, tone, prio = "对话中断", "err", 1
			}
			kbID := ""
			if row.Review != nil && row.Review.LibraryID != "" {
				kbID = row.Review.LibraryID
			} else if len(row.libraries) > 0 {
				kbID = row.libraries[0]
			}
			kbName := kbNames[kbID]
			if kbName == "" {
				kbName = kbID
			}
			title := limitQualityText(row.Question, 72)
			if row.Review != nil && row.Review.Reason != "" {
				title = limitQualityText(row.Question, 64)
			}
			summary.ActionStream = append(summary.ActionStream, workbenchActionItem{
				Kind:       "qa",
				Priority:   prio,
				ID:         row.ID,
				Title:      title,
				Badge:      badge,
				BadgeTone:  tone,
				SourceType: sourceTypeLabel(row.SourceType),
				Place:      row.ScopeName,
				ScopeID:    row.ScopeID,
				ChannelID:  row.ChannelID,
				KBID:       kbID,
				KBName:     kbName,
				MetaTop:    "AI 建议处理",
				MetaSub:    row.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("01/02 15:04"),
				SubText:    joinNonEmpty(" · ", sourceTypeLabel(row.SourceType), row.ScopeName, kbName),
				CTA:        "一键转问题",
				Primary:    true,
			})
		}
	}

	repliedCount := 0
	for _, iss := range issuePage.Items {
		if iss.Status == "resolved" || iss.Status == "closed" || iss.ArchiveState != "" {
			continue
		}
		if f.SourceType == "dm" && !iss.IsDirect {
			continue
		}
		if f.SourceType == "group" && (iss.IsDirect || iss.SubareaID != "") {
			continue
		}
		if f.SourceType == "subarea" && (iss.IsDirect || iss.SubareaID == "") {
			continue
		}
		if f.ChannelID != "" && iss.ChannelID != f.ChannelID {
			continue
		}
		summary.OpenIssues++
		rCnt := replyCounts[iss.ID]
		if rCnt > 0 {
			repliedCount++
		}
		stType := "主群"
		if iss.IsDirect {
			stType = "私聊"
		} else if iss.SubareaID != "" {
			stType = "子区"
		}
		place := iss.ScopeName
		if place == "" {
			place = scopeNames[iss.ScopeID]
		}
		kbName := kbNames[iss.KnowledgeBaseID]
		if kbName == "" {
			kbName = iss.KnowledgeBaseID
		}
		metaTop, cta, prio := "待处理", "复测 / 回告", 3
		if rCnt > 0 {
			metaTop, cta = "已回告待办结", "去办结"
		} else if iss.MessageID == "" {
			metaTop = "缺原消息"
		}
		summary.ActionStream = append(summary.ActionStream, workbenchActionItem{
			Kind:       "issue",
			Priority:   prio,
			ID:         iss.ID,
			Title:      iss.Title,
			Badge:      kbName,
			BadgeTone:  "brand",
			SourceType: stType,
			Place:      place,
			ScopeID:    iss.ScopeID,
			ChannelID:  iss.ChannelID,
			KBID:       iss.KnowledgeBaseID,
			KBName:     kbName,
			MetaTop:    metaTop,
			MetaSub:    iss.CreatedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("01/02 15:04"),
			SubText:    joinNonEmpty(" · ", stType, place, kbName),
			CTA:        cta,
			Primary:    rCnt == 0 && iss.MessageID != "",
		})
	}

	sort.SliceStable(summary.ActionStream, func(i, j int) bool {
		return summary.ActionStream[i].Priority < summary.ActionStream[j].Priority
	})
	if len(summary.ActionStream) > 30 {
		summary.ActionStream = summary.ActionStream[:30]
	}

	for i, g := range qPage.Stats.Groups {
		if i >= 5 {
			break
		}
		kbName := ""
		if len(g.libraries) > 0 {
			kbName = kbNames[g.libraries[0]]
		}
		st := "群聊"
		if strings.HasPrefix(g.ID, "dm:") {
			st = "私聊"
		}
		summary.HotQuestions = append(summary.HotQuestions, workbenchHotQuestion{
			ID:         g.Example,
			Question:   g.Question,
			KBName:     kbName,
			SourceType: st,
			Count:      g.Count,
			Users:      g.Users,
		})
	}

	summary.PendingQA = pendingQA
	summary.Alerts.InterruptedCount = interrupted
	summary.Alerts.FailedAIReviews = failedAI
	summary.KPIs.AIProcessCount = aiProc
	summary.KPIs.AIConvertedCount = aiConv
	summary.KPIs.OpenIssues = summary.OpenIssues
	summary.KPIs.RepliedIssues = repliedCount
	summary.KPIs.InterruptedCount = interrupted
	summary.KPIs.FailedAIReviews = failedAI
	if totalTurns > 0 {
		summary.KPIs.InterruptedRate = float64(interrupted) * 100 / float64(totalTurns)
	}
	return summary, nil
}

func (s *Service) collectKBHealth(ctx context.Context, tenant uint64, filterKB string) []kbHealthSummary {
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	var kbs []struct {
		ID, Name string
	}
	q := quiet.Table("knowledge_bases").Select("id, name").Where("tenant_id = ? AND deleted_at IS NULL", tenant)
	if filterKB != "" {
		q = q.Where("id = ?", filterKB)
	}
	if err := q.Order("name, id").Limit(50).Find(&kbs).Error; err != nil {
		return []kbHealthSummary{}
	}

	docStats := map[string]struct{ total, ready, disabled, pptx, xlsx int }{}
	var docs []struct {
		KnowledgeBaseID, EnableStatus, ParseStatus, FileType string
	}
	if quiet.Table("knowledges").Select("knowledge_base_id, enable_status, parse_status, file_type").
		Where("tenant_id = ? AND deleted_at IS NULL", tenant).Find(&docs).Error == nil {
		for _, d := range docs {
			st := docStats[d.KnowledgeBaseID]
			st.total++
			if d.EnableStatus == "disabled" {
				st.disabled++
			} else if d.ParseStatus == "completed" {
				st.ready++
			}
			ft := strings.ToLower(strings.TrimSpace(d.FileType))
			if ft == "pptx" || ft == "ppt" {
				st.pptx++
			} else if ft == "xlsx" || ft == "xls" || ft == "csv" {
				st.xlsx++
			}
			docStats[d.KnowledgeBaseID] = st
		}
	}

	wikiCounts := map[string]int{}
	var wikis []struct {
		KnowledgeBaseID string
		Cnt             int
	}
	if quiet.Table("wiki_pages").Select("knowledge_base_id, COUNT(*) AS cnt").
		Where("tenant_id = ? AND status = 'published' AND deleted_at IS NULL", tenant).
		Group("knowledge_base_id").Find(&wikis).Error == nil {
		for _, w := range wikis {
			wikiCounts[w.KnowledgeBaseID] = w.Cnt
		}
	}

	syncStats := map[string]struct {
		status, schedule, lastAt string
		partial14d               int
	}{}
	var sources []struct {
		ID, KnowledgeBaseID, SyncSchedule string
		LastSyncAt                        *time.Time
	}
	if quiet.Table("data_sources").Select("id, knowledge_base_id, sync_schedule, last_sync_at").
		Where("tenant_id = ? AND deleted_at IS NULL", tenant).Find(&sources).Error == nil && len(sources) > 0 {
		sourceToKB := map[string]string{}
		for _, src := range sources {
			sourceToKB[src.ID] = src.KnowledgeBaseID
			st := syncStats[src.KnowledgeBaseID]
			st.status = "ok"
			if src.SyncSchedule != "" {
				st.schedule = src.SyncSchedule
			}
			if src.LastSyncAt != nil {
				st.lastAt = src.LastSyncAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("01/02 15:04")
			}
			syncStats[src.KnowledgeBaseID] = st
		}
		since := time.Now().UTC().AddDate(0, 0, -14)
		var logs []struct {
			DataSourceID, Status string
		}
		if quiet.Table("sync_logs").Select("data_source_id, status").
			Where("created_at >= ? AND status IN ?", since, []string{"partial", "failed"}).Find(&logs).Error == nil {
			for _, lg := range logs {
				kbID := sourceToKB[lg.DataSourceID]
				if kbID == "" {
					continue
				}
				st := syncStats[kbID]
				st.partial14d++
				if lg.Status == "failed" && st.status != "partial" {
					st.status = "failed"
				} else {
					st.status = "partial"
				}
				syncStats[kbID] = st
			}
		}
	}

	out := make([]kbHealthSummary, 0, len(kbs))
	for _, kb := range kbs {
		if strings.HasPrefix(kb.Name, "__") {
			continue
		}
		ds := docStats[kb.ID]
		ss := syncStats[kb.ID]
		status := ss.status
		if status == "" {
			status = "ok"
		}
		note := "同步正常"
		if ss.partial14d > 0 {
			note = "近 14 天存在部分同步失败"
		}
		if ds.disabled > 0 {
			note += "（含 disabled 文档）"
		}
		out = append(out, kbHealthSummary{
			ID:             kb.ID,
			Name:           kb.Name,
			Docs:           ds.total,
			Ready:          ds.ready,
			Disabled:       ds.disabled,
			PPTX:           ds.pptx,
			XLSX:           ds.xlsx,
			WikiPages:      wikiCounts[kb.ID],
			SyncStatus:     status,
			PartialRuns14d: ss.partial14d,
			LastSyncAt:     ss.lastAt,
			Schedule:       ss.schedule,
			SyncNote:       note,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Docs > out[j].Docs
	})
	return out
}

func (s *Service) issueReplyCounts(ctx context.Context, tenant uint64, issues []Issue) map[string]int {
	out := map[string]int{}
	if len(issues) == 0 {
		return out
	}
	ids := make([]string, 0, len(issues))
	for _, i := range issues {
		ids = append(ids, i.ID)
	}
	var rows []struct {
		IssueID string
		Cnt     int
	}
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	if quiet.Table("octo_issue_replies").Select("issue_id, COUNT(*) AS cnt").
		Where("tenant_id = ? AND issue_id IN ? AND status = 'sent'", tenant, ids).
		Group("issue_id").Find(&rows).Error == nil {
		for _, r := range rows {
			out[r.IssueID] = r.Cnt
		}
	}
	return out
}

func (s *Service) scopeDisplayMap(ctx context.Context, tenant uint64) map[string]string {
	out := map[string]string{}
	var scopes []octointegration.Scope
	quiet := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	if quiet.Where("tenant_id = ?", tenant).Find(&scopes).Error == nil {
		for _, sc := range scopes {
			out[sc.ID] = sc.DisplayName
		}
	}
	return out
}

func sourceTypeLabel(st string) string {
	switch st {
	case "dm":
		return "私聊"
	case "subarea":
		return "子区"
	default:
		return "主群"
	}
}

func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, sep)
}

func parseAgentTraceSummary(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var steps []struct {
		ToolCalls []struct {
			Name string `json:"name"`
		} `json:"tool_calls"`
		Thought string `json:"thought"`
	}
	if json.Unmarshal([]byte(raw), &steps) != nil {
		return nil
	}
	out := []string{}
	for _, st := range steps {
		for _, tc := range st.ToolCalls {
			if tc.Name != "" && len(out) < 8 {
				out = append(out, tc.Name)
			}
		}
	}
	return out
}
