package octobusiness

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChineseReportArtifactsKeepAPIKeysAndZonedTime(t *testing.T) {
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	end := start.AddDate(0, 0, 7)
	report := &Report{From: &start, To: &end, GeneratedAt: end, Scope: "测试群", Counts: map[string]int64{"open": 1}, Items: []Issue{{ID: "issue-1", Kind: "missing", Title: "=需要确认的中文问题", ReporterName: "提问人甲", OwnerName: "负责人乙", Status: "open", CreatedAt: start}}, Truncated: true}
	for _, format := range []string{"html", "markdown", "csv"} {
		file, err := ReportFile(report, format)
		require.NoError(t, err)
		require.Contains(t, file.Content, "待处理")
		require.Contains(t, file.Content, "+08:00")
		require.Contains(t, file.Content, start.Format(time.RFC3339))
		require.Contains(t, file.Content, "清单已截断")
		require.NotContains(t, file.Content, "List truncated")
		if format == "csv" {
			require.True(t, strings.HasPrefix(file.Content, "\uFEFF"))
			rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(file.Content, "\uFEFF"))).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 3)
			require.Equal(t, []string{"编号", "类型", "问题", "提问人", "负责人", "状态", "登记时间", "来源消息", "统计范围", "统计周期"}, rows[0])
			require.Equal(t, "知识缺口", rows[1][1])
			require.Equal(t, "'=需要确认的中文问题", rows[1][2])
			require.Equal(t, "待处理", rows[1][5])
		}
	}
	require.Equal(t, "open", report.Items[0].Status)
	require.Equal(t, "missing", report.Items[0].Kind)
	require.EqualValues(t, 1, report.Counts["open"], "API status keys are unchanged")
}
