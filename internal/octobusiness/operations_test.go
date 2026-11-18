package octobusiness

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOperationsReportMonthEndAndCalendarWindow(t *testing.T) {
	at := time.Date(2026, time.February, 27, 7, 0, 0, 0, time.UTC)
	plan := ReportSchedule{Timezone: "Asia/Shanghai", Frequency: "monthly", MonthDay: 31, Hour: 9, Period: "previous_month"}
	next, err := nextReportTime(plan, at)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, time.February, 28, 1, 0, 0, 0, time.UTC), next)
	from, to, err := reportWindow(plan, next, nil, nil)
	require.NoError(t, err)
	require.Equal(t, time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC), from)
	require.Equal(t, time.Date(2026, time.January, 31, 16, 0, 0, 0, time.UTC), to)
}

func TestArchiveBatchKeepsHistoryAndRejectsStaleRow(t *testing.T) {
	s := testService(t)
	issue, err := s.CreateIssue(principalContext(testPrincipal()), gapInput())
	require.NoError(t, err)
	c := consoleTestContext()
	old := issueBatchTarget{ID: issue.ID, UpdatedAt: issue.UpdatedAt}
	results, err := s.batchIssues(c, issueBatchInput{Targets: []issueBatchTarget{old}, Action: "archive"})
	require.NoError(t, err)
	require.Equal(t, "updated", results[0].State)
	results, err = s.batchIssues(c, issueBatchInput{Targets: []issueBatchTarget{old}, Action: "trash"})
	require.NoError(t, err)
	require.Equal(t, "changed", results[0].State)
	archived, err := s.ListIssues(c, IssueFilter{Visibility: "archive"})
	require.NoError(t, err)
	require.Len(t, archived.Items, 1)
	report, err := s.Report(c, IssueFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, report.Total)
	active, err := s.ListIssues(c, IssueFilter{})
	require.NoError(t, err)
	require.Empty(t, active.Items)
}

func TestSimilarQuestionSuggestionsRespectHistoricLibraries(t *testing.T) {
	scope := &qualityScope{}
	rows := []qualityRow{
		{ID: "a", GroupKey: "a", Question: "如何查看广告投放报表", libraries: []string{"A"}},
		{ID: "b", GroupKey: "b", Question: "在哪里查看广告投放报表", libraries: []string{"A"}},
		{ID: "c", GroupKey: "c", Question: "如何查看广告投放报表", libraries: []string{"B"}},
	}
	require.False(t, scope.permits("A"))
	stats := qualityStats(rows)
	require.Len(t, stats.Similar, 1)
	require.Equal(t, "a", stats.Similar[0].A.Example)
	require.Equal(t, "b", stats.Similar[0].B.Example)
}
