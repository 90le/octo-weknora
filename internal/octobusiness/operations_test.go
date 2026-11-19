package octobusiness

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestOperationsReportMonthEndAndCalendarWindow(t *testing.T) {
	at := time.Date(2026, time.February, 27, 7, 0, 0, 0, time.UTC)
	plan := ReportSchedule{
		Timezone: "Asia/Shanghai", Frequency: "monthly", MonthDay: 31, Hour: 9, Period: "previous_month",
	}
	next, err := nextReportTime(plan, at)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, time.February, 28, 1, 0, 0, 0, time.UTC), next)
	from, to, err := reportWindow(plan, next, nil, nil)
	require.NoError(t, err)
	require.Equal(t, time.Date(2025, time.December, 31, 16, 0, 0, 0, time.UTC), from)
	require.Equal(t, time.Date(2026, time.January, 31, 16, 0, 0, 0, time.UTC), to)
}

func TestAnomalyQuietHoursCooldownAndCoverage(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	plan := ReportSchedule{
		Template: "anomaly", Timezone: "Asia/Shanghai", Threshold: 2, MinimumUsers: 2,
		QuietStart: 22, QuietEnd: 8, CooldownHours: 6,
	}
	due := time.Date(2026, time.November, 18, 15, 0, 0, 0, time.UTC)
	run := ReportRun{ID: "candidate", TenantID: 1, ScheduleID: "plan", DueAt: due}
	report := &Report{Operations: &operationalReport{Metrics: map[string]int64{"attention": 2, "attention_people": 2}}}
	reason, err := s.reportShouldSkip(ctx, plan, report, run)
	require.NoError(t, err)
	require.Equal(t, "quiet_hours", reason)
	run.DueAt = due.Add(10 * time.Hour)
	old := ReportRun{ID: "earlier", TenantID: 1, ScheduleID: "plan", DueAt: run.DueAt.Add(-time.Hour), Status: "sent"}
	require.NoError(t, s.db.Create(&old).Error)
	reason, err = s.reportShouldSkip(ctx, plan, report, run)
	require.NoError(t, err)
	require.Equal(t, "cooldown", reason)
	report.Operations.Limited = true
	reason, err = s.reportShouldSkip(ctx, plan, report, run)
	require.NoError(t, err)
	require.Equal(t, "incomplete_sample", reason)
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

func TestWorkspaceAdminCanOrganizeIssuesAfterLibraryDeletion(t *testing.T) {
	s := testService(t)
	member := principalContext(testPrincipal())
	issue, err := s.CreateIssue(member, gapInput())
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("UPDATE knowledge_bases SET deleted_at = ? WHERE id = ?", time.Now(), "kb-a").Error)
	admin := consoleTestContext()
	target := issueBatchTarget{ID: issue.ID, UpdatedAt: issue.UpdatedAt}
	input := issueBatchInput{Targets: []issueBatchTarget{target}, Action: "archive"}

	_, err = s.batchIssues(member, input)
	require.ErrorIs(t, err, ErrDenied)
	foreign := testPrincipal()
	foreign.TenantID, foreign.Console = 2, true
	keyAdmin := types.WithTenantAPIKeyScope(admin, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-b"}})
	for _, unauthorized := range []context.Context{principalContext(foreign), keyAdmin} {
		result, err := s.batchIssues(unauthorized, input)
		require.NoError(t, err)
		require.Equal(t, "unavailable", result[0].State)
	}

	for _, step := range []struct{ action, state, result string }{
		{"status", "", "unavailable"},
		{"archive", "archived", "updated"},
		{"trash", "trash", "updated"},
		{"restore", "", "updated"},
	} {
		input.Action, input.Status = step.action, "resolved"
		result, err := s.batchIssues(admin, input)
		require.NoError(t, err)
		require.Equal(t, step.result, result[0].State)
		updated, _, err := s.GetIssue(admin, issue.ID)
		require.NoError(t, err)
		require.Equal(t, step.state, updated.ArchiveState)
		require.Equal(t, issue.Status, updated.Status)
		require.Equal(t, issue.KnowledgeBaseID, updated.KnowledgeBaseID)
		input.Targets[0].UpdatedAt = updated.UpdatedAt
	}
	_, events, err := s.GetIssue(admin, issue.ID)
	require.NoError(t, err)
	require.Len(t, events, 4)
	require.Equal(t, "archive ", events[1].Note)
	require.Equal(t, "trash ", events[2].Note)
	require.Equal(t, "restore ", events[3].Note)
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
