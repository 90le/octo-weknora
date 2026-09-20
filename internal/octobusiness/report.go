package octobusiness

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"strings"
	"time"
)

type Report struct {
	Counts      map[string]int64 `json:"counts"`
	Items       []Issue          `json:"items"`
	Total       int64            `json:"total"`
	Truncated   bool             `json:"truncated"`
	From        *time.Time       `json:"from"`
	To          *time.Time       `json:"to"`
	GeneratedAt time.Time        `json:"generated_at"`
	Scope       string           `json:"scope"`
	Notice      string           `json:"notice"`
}

func (s *Service) Report(ctx context.Context, f IssueFilter) (*Report, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		return nil, ErrInvalid
	}
	r := &Report{Counts: map[string]int64{}, Items: []Issue{}, From: f.From, To: f.To, GeneratedAt: time.Now().UTC(), Scope: p.ScopeName, Notice: "仅统计已登记的问题，不代表全部提问量或总体解答率。关闭问题不代表知识已更新。统计周期按登记时间筛选，不含结束时刻。"}
	if p.IsDirect {
		r.Scope = "当前私聊"
	}
	if p.Console {
		r.Scope = "当前获授权工作区的问题"
	}
	var groups []struct {
		Status string
		Count  int64
	}
	if err = s.issueQuery(ctx, p, f).Select("status, count(*) AS count").Group("status").Scan(&groups).Error; err != nil {
		return nil, err
	}
	for _, group := range groups {
		r.Counts[group.Status] = group.Count
		r.Total += group.Count
	}
	err = s.issueQuery(ctx, p, f).Order("created_at DESC, id").Limit(500).Find(&r.Items).Error
	r.Truncated = r.Total > int64(len(r.Items))
	return r, err
}

// ReportArtifact contains generated data only, never a server filesystem path.
type ReportArtifact struct {
	Name        string `json:"name"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
}

func ReportFile(r *Report, format string) (*ReportArtifact, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	file := &ReportArtifact{Name: "knowledge-issues-" + r.GeneratedAt.Format("20060102") + "." + format}
	var b strings.Builder
	from, to := "未限定起点", "未限定终点"
	if r.From != nil {
		from = r.From.Format(time.RFC3339)
	}
	if r.To != nil {
		to = r.To.Format(time.RFC3339)
	}
	period := from + " 至 " + to + "（右端不含）"
	switch format {
	case "csv":
		file.ContentType = "text/csv; charset=utf-8"
		// Excel on Windows recognizes UTF-8 Chinese reliably with a BOM.
		b.WriteString("\uFEFF")
		writer := csv.NewWriter(&b)
		_ = writer.Write([]string{"编号", "类型", "问题", "提问人", "负责人", "状态", "登记时间", "来源消息", "统计范围", "统计周期"})
		for _, row := range r.Items {
			_ = writer.Write([]string{safeCSV(row.ID), safeCSV(issueKindLabel(row.Kind)), safeCSV(row.Title), safeCSV(row.ReporterName), safeCSV(row.OwnerName), safeCSV(issueStatusLabel(row.Status)), row.CreatedAt.Format(time.RFC3339), safeCSV(row.MessageID), safeCSV(r.Scope), period})
		}
		if r.Truncated {
			note := make([]string, 10)
			note[0] = "说明"
			note[2] = "清单已截断，仅显示最近 500 项；统计数量仍为完整结果。"
			_ = writer.Write(note)
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, err
		}
	case "html":
		file.ContentType = "text/html; charset=utf-8"
		b.WriteString(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'"><title>知识问题报告</title><style>body{font:16px system-ui;max-width:1100px;margin:40px auto;padding:20px;color:#17212d}table{border-collapse:collapse;width:100%}th,td{padding:12px;text-align:left;border-bottom:1px solid #ddd}small{color:#556}</style><h1>知识问题报告</h1>`)
		fmt.Fprintf(&b, "<p>%s · 生成时间 %s · 已登记 %d 项</p><p>统计周期：%s</p><table><tr><th>问题</th><th>提问人</th><th>负责人</th><th>状态</th></tr>", html.EscapeString(r.Scope), r.GeneratedAt.Format(time.RFC3339), r.Total, html.EscapeString(period))
		for _, row := range r.Items {
			fmt.Fprintf(&b, "<tr><td>%s<br><small>%s</small></td><td>%s</td><td>%s</td><td>%s</td></tr>", html.EscapeString(row.Title), html.EscapeString(row.ID), html.EscapeString(row.ReporterName), html.EscapeString(row.OwnerName), html.EscapeString(issueStatusLabel(row.Status)))
		}
		b.WriteString("</table><p>" + html.EscapeString(r.Notice) + "</p>")
	case "markdown":
		file.Name = strings.TrimSuffix(file.Name, "markdown") + "md"
		file.ContentType = "text/markdown; charset=utf-8"
		fmt.Fprintf(&b, "# 知识问题报告\n\n范围：%s\n\n统计周期：%s\n\n登记问题：%d\n\n", r.Scope, period, r.Total)
		for _, row := range r.Items {
			fmt.Fprintf(&b, "- %s — %s；提问人：%s；负责人：%s；编号：%s\n", strings.ReplaceAll(row.Title, "\n", " "), issueStatusLabel(row.Status), row.ReporterName, row.OwnerName, row.ID)
		}
		b.WriteString("\n" + r.Notice + "\n")
	default:
		return nil, ErrInvalid
	}
	if r.Truncated {
		if format == "html" {
			b.WriteString("<p>清单已截断，仅显示最近 500 项；统计数量仍为完整结果。</p>")
		} else if format != "csv" {
			b.WriteString("\n清单已截断，仅显示最近 500 项；统计数量仍为完整结果。\n")
		}
	}
	file.Content = b.String()
	return file, nil
}

func issueStatusLabel(status string) string {
	switch status {
	case "open":
		return "待处理"
	case "inprogress":
		return "处理中"
	case "resolved":
		return "已解决"
	case "closed":
		return "已关闭"
	default:
		return status
	}
}
func issueKindLabel(kind string) string {
	switch kind {
	case "missing":
		return "知识缺口"
	case "bug":
		return "Bug 反馈"
	case "suggestion":
		return "建议"
	default:
		return kind
	}
}
func safeCSV(s string) string {
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed != "" && strings.ContainsAny(trimmed[:1], "=+-@") {
		return "'" + s
	}
	return s
}
