package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"gorm.io/gorm"
)

type reportAccessError string

func (e reportAccessError) Error() string { return string(e) }
func (e reportAccessError) Unwrap() error { return ErrDenied }

// ReportAccessError gives operators a field-specific, safe diagnosis.
func ReportAccessError(code string) error { return reportAccessError(code) }

func reportErrorCode(err error) string {
	var access reportAccessError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &access):
		return string(access)
	case errors.Is(err, errReportScheduleChanged):
		return "plan_changed"
	case errors.Is(err, ErrDenied):
		return "authorization"
	default:
		return "delivery_unconfirmed"
	}
}

func (s ReportSchedule) validationSource() ReportSchedule {
	if s.Mode == "generate" {
		s.RecipientType, s.RecipientUID = "source", ""
	}
	return s
}

func normalizeReportSchedule(s *ReportSchedule, now time.Time) (time.Time, error) {
	periods := []string{"", "last_days", "yesterday", "previous_week", "previous_month", "since_last", "custom"}
	if !validText(s.Name, 256) || !validText(s.ChannelID, 128) || !validText(s.ScopeID, 128) ||
		!contains([]string{"source", "private"}, s.RecipientType) ||
		(s.RecipientType == "private" && s.Mode != "generate" && !validText(s.RecipientUID, 128)) ||
		!contains([]string{"text", "markdown", "html", "csv"}, s.Format) ||
		!contains([]string{"", "maintenance", "quality", "topic", "anomaly"}, s.Template) ||
		!contains([]string{"", "weekly", "daily", "weekdays", "monthly", "interval", "manual"}, s.Frequency) ||
		!contains(periods, s.Period) ||
		!contains([]string{"", "generate", "approval", "auto"}, s.Mode) ||
		(s.Period == "custom" && s.Frequency != "manual") || len(s.Instruction) > 2500 ||
		s.Lookback < 0 || s.Lookback > 93 || s.Threshold < 0 || s.Threshold > 12000 ||
		s.MinimumUsers < 0 || s.MinimumUsers > 12000 || s.CooldownHours < 0 || s.CooldownHours > 720 ||
		s.QuietStart < 0 || s.QuietStart > 23 || s.QuietEnd < 0 || s.QuietEnd > 23 || len(s.Topic) > 160 {
		return time.Time{}, ErrInvalid
	}
	s.Topic = strings.TrimSpace(s.Topic)
	if s.RecipientType == "source" {
		s.RecipientUID = ""
	}
	if s.Timezone == "" {
		s.Timezone = "Asia/Shanghai"
	}
	return nextReportTime(*s, now)
}

func nextReportTime(s ReportSchedule, after time.Time) (time.Time, error) {
	if s.Frequency == "" || s.Frequency == "weekly" {
		return nextWeekly(s, after)
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil || s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 {
		return time.Time{}, ErrInvalid
	}
	day := after.In(loc)
	switch s.Frequency {
	case "manual":
		return time.Time{}, nil
	case "interval":
		if s.IntervalDays < 1 || s.IntervalDays > 90 {
			return time.Time{}, ErrInvalid
		}
		day = day.AddDate(0, 0, s.IntervalDays)
		return time.Date(day.Year(), day.Month(), day.Day(), s.Hour, s.Minute, 0, 0, loc).UTC(), nil
	case "monthly":
		if s.MonthDay < 1 || s.MonthDay > 31 {
			return time.Time{}, ErrInvalid
		}
		for offset := 0; offset <= 1; offset++ {
			month := time.Date(day.Year(), day.Month()+time.Month(offset), 1, s.Hour, s.Minute, 0, 0, loc)
			last := month.AddDate(0, 1, -1).Day()
			at := month.AddDate(0, 0, min(last, s.MonthDay)-1)
			if at.After(after) {
				return at.UTC(), nil
			}
		}
	case "daily", "weekdays":
		for offset := 0; offset <= 3; offset++ {
			d := day.AddDate(0, 0, offset)
			at := time.Date(d.Year(), d.Month(), d.Day(), s.Hour, s.Minute, 0, 0, loc)
			weekend := at.Weekday() == time.Saturday || at.Weekday() == time.Sunday
			if at.After(after) && (s.Frequency == "daily" || !weekend) {
				return at.UTC(), nil
			}
		}
	}
	return time.Time{}, ErrInvalid
}

func reportWindow(s ReportSchedule, due time.Time, start, end *time.Time) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	t := due.In(loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	switch s.Period {
	case "yesterday":
		return midnight.AddDate(0, 0, -1).UTC(), midnight.UTC(), nil
	case "previous_week":
		monday := midnight.AddDate(0, 0, -(int(t.Weekday())+6)%7)
		return monday.AddDate(0, 0, -7).UTC(), monday.UTC(), nil
	case "previous_month":
		month := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		return month.AddDate(0, -1, 0).UTC(), month.UTC(), nil
	case "custom":
		if start == nil || end == nil || !start.Before(*end) ||
			end.After(start.AddDate(0, 0, 93)) || end.After(due.Add(time.Minute)) {
			return time.Time{}, time.Time{}, ErrInvalid
		}
		return start.UTC(), end.UTC(), nil
	}
	n := s.Lookback
	if n == 0 {
		n = 7
	}
	return t.AddDate(0, 0, -n).UTC(), due.UTC(), nil
}

func (s *Service) reportPeriod(
	ctx context.Context, plan ReportSchedule, due time.Time, start, end *time.Time,
) (time.Time, time.Time, error) {
	if start != nil || end != nil {
		plan.Period = "custom"
	}
	from, to, err := reportWindow(plan, due, start, end)
	if err != nil || plan.Period != "since_last" {
		return from, to, err
	}
	var last ReportRun
	result := s.db.WithContext(ctx).Where("tenant_id = ? AND schedule_id = ? AND to_at < ? AND status IN ?",
		plan.TenantID, plan.ID, to, []string{"generated", "pending", "sent", "skipped"}).Order("to_at DESC").Take(&last)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return from, to, result.Error
	}
	if last.ToAt != nil && last.ToAt.After(to.AddDate(0, 0, -93)) {
		from = *last.ToAt
	}
	return from, to, nil
}

type operationalReport struct {
	Template string           `json:"template"`
	Metrics  map[string]int64 `json:"metrics"`
	Topics   []qualityBucket  `json:"topics"`
	Limited  bool             `json:"limited"`
	Analysis string           `json:"analysis"`
	Advice   []string         `json:"advice"`
	Previous map[string]int64 `json:"previous,omitempty"`
}

var reportMetricNames = []struct{ key, label string }{
	{"questions", "提问量"},
	{"people", "提问人数"},
	{"attention", "待关注问答"},
	{"attention_people", "待关注提问人数"},
	{"repeated", "反复疑似未答问答"},
	{"reviewed", "已复核问答"},
	{"unavailable", "资料不可用问答"},
	{"failed", "处理或检索异常"},
	{"opened", "新登记问题"},
	{"resolved", "期内标记解决"},
	{"backlog", "期末未解决"},
	{"unassigned", "待分派问题（当前）"},
}

func (s *Service) prepareReport(
	ctx context.Context, p Principal, plan ReportSchedule, run *ReportRun, start, end *time.Time,
) (*Report, error) {
	from, to, err := s.reportPeriod(ctx, plan, run.DueAt, start, end)
	if err != nil {
		return nil, err
	}
	filter := IssueFilter{From: &from, To: &to}
	if plan.Template != "" {
		filter.ScopeID = plan.ScopeID
		filter.exactChannelID, filter.exactLibraries = p.ChannelID, p.KnowledgeBaseIDs
	}
	report, err := s.Report(ctx, filter)
	if err != nil {
		return nil, err
	}
	if plan.Template != "" {
		if report.Operations, err = s.summarizeOperations(ctx, p, plan, report, from, to); err != nil {
			return nil, err
		}
		// Operational delivery uses aggregate metrics. Console evidence is separately authorized.
		report.Items = []Issue{}
	}
	snapshot, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	signatureBody, err := json.Marshal(report.Operations)
	if err != nil {
		return nil, err
	}
	run.Authority, run.Snapshot = reportAuthority(p), string(snapshot)
	run.Fingerprint = digest(string(signatureBody))
	if report.Operations != nil {
		fixedFacts, _ := json.Marshal(struct {
			Template string
			Metrics  map[string]int64
			Topics   []qualityBucket
		}{report.Operations.Template, report.Operations.Metrics, report.Operations.Topics})
		run.Fingerprint = digest(string(fixedFacts))
	}
	run.FromAt, run.ToAt = &from, &to
	run.Target = "原群／子区"
	if plan.Mode == "generate" {
		run.Target = "工作台"
	} else if plan.RecipientType == "private" {
		run.Target = "私聊 · " + plan.RecipientUID
	}
	err = s.db.WithContext(ctx).Model(&ReportRun{}).Where("tenant_id = ? AND id = ?", run.TenantID, run.ID).
		UpdateColumns(map[string]any{
			"from_at": from, "to_at": to, "authority": run.Authority, "snapshot": run.Snapshot,
			"fingerprint": run.Fingerprint, "target": run.Target,
		}).Error
	return report, err
}

func (s *Service) summarizeOperations(
	ctx context.Context, p Principal, plan ReportSchedule, report *Report, from, to time.Time,
) (*operationalReport, error) {
	scope, err := octointegration.NewStore(s.db).Get(ctx, p.TenantID, plan.ScopeID)
	if err != nil || scope.AccountID != p.AccountID || p.ScopeID != scope.ID || p.ChannelID != plan.ChannelID {
		return nil, ErrDenied
	}
	q := &qualityScope{Scope: scope, ChannelID: p.ChannelID}
	for _, k := range p.KnowledgeBaseIDs {
		q.Libraries = append(q.Libraries, qualityLibrary{ID: k})
	}
	rows, limited, err := s.collectQuality(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	if err = s.enrichQuality(ctx, q, rows); err != nil {
		return nil, err
	}
	if plan.Template == "topic" && plan.Topic != "" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.Review != nil && row.Review.Topic == plan.Topic {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	repeats := map[string]int{}
	for _, row := range rows {
		if row.Outcome == "suspected_gap" {
			repeats[row.GroupKey]++
		}
	}
	repeated := 0
	attentionPeople := map[string]bool{}
	for i := range rows {
		rows[i].Repeats = repeats[rows[i].GroupKey]
		if rows[i].Outcome == "suspected_gap" && rows[i].Repeats > 1 {
			repeated++
		}
		if qualityMatches(rows[i], "attention") {
			attentionPeople[rows[i].userID] = true
		}
	}
	stats := qualityStats(rows)
	out := &operationalReport{
		Template: plan.Template, Topics: stats.Topics[:min(5, len(stats.Topics))], Limited: limited, Analysis: "off",
		Metrics: map[string]int64{
			"questions": int64(stats.Questions), "people": int64(stats.Users), "attention": int64(stats.NeedsReview),
			"repeated": int64(repeated), "reviewed": int64(stats.Reviewed), "unavailable": int64(stats.Unavailable),
			"failed": int64(stats.Failed), "opened": report.Total, "attention_people": int64(len(attentionPeople)),
		},
	}
	scopeFilter := IssueFilter{
		ScopeID: plan.ScopeID, To: &to, Visibility: "all",
		exactChannelID: p.ChannelID, exactLibraries: p.KnowledgeBaseIDs,
	}
	issues := s.issueQuery(ctx, p, scopeFilter)
	issueIDs := issues.Session(&gorm.Session{}).Select("id")
	events := s.db.WithContext(ctx).Model(&IssueEvent{}).
		Where("tenant_id = ? AND issue_id IN (?)", p.TenantID, issueIDs)
	var resolved, backlog, unassigned int64
	if err = events.Session(&gorm.Session{}).
		Where("status = 'resolved' AND created_at >= ? AND created_at < ?", from, to).
		Distinct("issue_id").Count(&resolved).Error; err != nil {
		return nil, err
	}
	if err = issues.Session(&gorm.Session{}).Where(`COALESCE((SELECT e.status FROM octo_knowledge_issue_events e
	   WHERE e.tenant_id = octo_knowledge_issues.tenant_id
	     AND e.issue_id = octo_knowledge_issues.id AND e.created_at < ?
	   ORDER BY e.created_at DESC, e.id DESC LIMIT 1), 'open') IN ('open','inprogress')`, to).
		Count(&backlog).Error; err != nil {
		return nil, err
	}
	if err = issues.Session(&gorm.Session{}).Where("owner_uid = '' AND status IN ('open','inprogress')").
		Count(&unassigned).Error; err != nil {
		return nil, err
	}
	out.Metrics["resolved"], out.Metrics["backlog"], out.Metrics["unassigned"] = resolved, backlog, unassigned
	if plan.Template == "quality" && !limited {
		previous, incomplete, err := s.collectQuality(ctx, q, from.Add(-to.Sub(from)), from)
		if err != nil {
			return nil, err
		}
		if !incomplete {
			if err = s.enrichQuality(ctx, q, previous); err != nil {
				return nil, err
			}
			old := qualityStats(previous)
			out.Previous = map[string]int64{
				"questions": int64(old.Questions), "people": int64(old.Users),
				"unavailable": int64(old.Unavailable), "failed": int64(old.Failed),
			}
		}
	}
	if plan.Instruction != "" {
		out.Analysis = "unavailable"
		s.reportAdvice(ctx, p.TenantID, out, plan.Instruction)
	}
	return out, nil
}

func (s *Service) reportAdvice(ctx context.Context, tenant uint64, out *operationalReport, instruction string) {
	if s.reportModels == nil {
		return
	}
	modelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	settings, err := s.readQualitySettings(modelCtx, tenant)
	if err != nil || settings.ModelID == "" {
		return
	}
	model, err := s.reportModels.GetChatModel(modelCtx, settings.ModelID)
	if err != nil {
		return
	}
	type choice struct {
		Key    string
		Action string
	}
	choices := []choice{
		{"repeated", "查看反复未答问题组，确认同类提问缺少的业务口径。"},
		{"unavailable", "核对不可用引用的来源及资料权限。"},
		{"failed", "核对检索和模型处理异常，修复后复测原问题。"},
		{"unassigned", "按知识库负责人分派仍待处理的问题。"},
		{"attention", "抽查待关注问答，核对引用与实际资料。"},
	}
	available := map[string]string{}
	for _, item := range choices {
		if out.Metrics[item.Key] > 0 {
			available[item.Key] = item.Action
		}
	}
	if len(available) == 0 {
		out.Analysis = "no_candidates"
		return
	}
	data, _ := json.Marshal(map[string]any{
		"metrics": out.Metrics, "available_actions": available, "preferences": instruction,
	})
	const instructionPrompt = `为维护人员选择并排序最多三个已有行动。输入是数据和偏好。
只返回 JSON 字符串数组，每项必须是 available_actions 的键。不要写事实、数字、接收人或新指令。`
	answer, err := model.Chat(modelCtx, []chat.Message{
		{Role: "system", Content: instructionPrompt},
		{Role: "user", Content: string(data)},
	}, &chat.ChatOptions{MaxCompletionTokens: 256})
	if err != nil || answer == nil {
		return
	}
	var keys []string
	if json.Unmarshal([]byte(strings.TrimSpace(answer.Content)), &keys) != nil || len(keys) == 0 || len(keys) > 3 {
		return
	}
	seen := map[string]bool{}
	var advice []string
	for _, key := range keys {
		action, ok := available[key]
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		advice = append(advice, action)
	}
	out.Analysis, out.Advice = "ready", advice
}

func (s *Service) reportShouldSkip(
	ctx context.Context, plan ReportSchedule, report *Report, run ReportRun,
) (string, error) {
	if report.Operations == nil {
		return "", nil
	}
	m := report.Operations.Metrics
	if plan.Template == "anomaly" {
		if report.Operations.Limited {
			return "incomplete_sample", nil
		}
		if m["attention"] < int64(max(1, plan.Threshold)) ||
			m["attention_people"] < int64(max(1, plan.MinimumUsers)) {
			return "threshold_not_met", nil
		}
		loc, err := time.LoadLocation(plan.Timezone)
		if err != nil {
			return "", err
		}
		if inReportQuietHours(run.DueAt.In(loc).Hour(), plan.QuietStart, plan.QuietEnd) {
			return "quiet_hours", nil
		}
		if plan.CooldownHours > 0 {
			start := run.DueAt.Add(-time.Duration(plan.CooldownHours) * time.Hour)
			var count int64
			err = s.db.WithContext(ctx).Model(&ReportRun{}).
				Where("tenant_id = ? AND schedule_id = ? AND due_at >= ? AND due_at < ?",
					run.TenantID, run.ScheduleID, start, run.DueAt).
				Where("status IN ?", []string{"sent", "generated", "pending", "uncertain"}).Count(&count).Error
			if err != nil || count > 0 {
				return "cooldown", err
			}
		}
	}
	if !plan.OnlyChanges {
		return "", nil
	}
	if m["questions"] == 0 && m["opened"] == 0 && m["backlog"] == 0 {
		return "no_activity", nil
	}
	var previous ReportRun
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND schedule_id = ? AND id <> ? AND status IN ?",
		run.TenantID, run.ScheduleID, run.ID, []string{"sent", "generated", "pending"}).
		Order("due_at DESC").Take(&previous).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if previous.Fingerprint == run.Fingerprint {
		return "unchanged", nil
	}
	return "", nil
}

func inReportQuietHours(hour, starts, ends int) bool {
	if starts == ends {
		return false
	}
	if starts < ends {
		return hour >= starts && hour < ends
	}
	return hour >= starts || hour < ends
}

func (r *operationalReport) markdown(report *Report) string {
	names := map[string]string{"maintenance": "维护待办", "quality": "知识质量回顾", "topic": "专题报告", "anomaly": "异常提醒"}
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n范围：%s\n\n周期：%s 至 %s（不含结束时间）\n\n",
		names[r.Template], report.Scope, report.From.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"),
		report.To.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"))
	order := map[string][]string{
		"maintenance": {"backlog", "unassigned", "opened", "resolved", "repeated", "unavailable"},
		"anomaly":     {"attention", "attention_people", "repeated", "failed", "unavailable"},
		"topic":       {"questions", "people", "attention", "repeated", "reviewed"},
	}
	for _, metric := range reportMetricNames {
		if keys := order[r.Template]; len(keys) != 0 && !contains(keys, metric.key) {
			continue
		}
		value := r.Metrics[metric.key]
		if prior, ok := r.Previous[metric.key]; ok {
			fmt.Fprintf(&out, "- %s：%d（上一等长周期 %d，变化 %+d）\n", metric.label, value, prior, value-prior)
			continue
		}
		fmt.Fprintf(&out, "- %s：%d\n", metric.label, value)
	}
	if r.Template == "topic" && len(r.Topics) != 0 {
		out.WriteString("\n## 业务主题\n\n")
		for _, topic := range r.Topics {
			label := limitQualityText(strings.Join(strings.Fields(topic.Label), " "), 80)
			fmt.Fprintf(&out, "- %s：%d\n", label, topic.Count)
		}
	}
	if len(r.Advice) > 0 {
		out.WriteString("\n## 建议安排\n\n")
		for _, advice := range r.Advice {
			fmt.Fprintf(&out, "- %s\n", advice)
		}
	} else if r.Analysis == "unavailable" {
		out.WriteString("\nAI 建议暂不可用。基础统计已生成。\n")
	}
	if r.Limited {
		out.WriteString("\n问答候选记录超过读取上限，可缩短周期重新生成。\n")
	}
	out.WriteString("\n问答量仅覆盖该区域留存的文字问答。")
	return out.String()
}
