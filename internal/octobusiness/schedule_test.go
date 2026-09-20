package octobusiness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type capturingQueue struct {
	tasks []*asynq.Task
	err   error
}

func (q *capturingQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	return nil, q.err
}
func consoleTestContext() context.Context {
	p := testPrincipal()
	p.Console = true
	p.ChannelID = "console"
	return principalContext(p)
}
func TestChildAggregationIsReadOnlyAndOptIn(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	child := p
	child.ScopeID = "child"
	child.ChannelID = "group-A&child"
	child.SubareaID = "child"
	issue, err := s.CreateIssue(principalContext(child), gapInput())
	require.NoError(t, err)
	without, err := s.ListIssues(principalContext(p), IssueFilter{})
	require.NoError(t, err)
	require.Zero(t, without.Total)
	p.ReadIssueScopeIDs = []string{"child"}
	with, err := s.ListIssues(principalContext(p), IssueFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, with.Total)
	_, _, err = s.GetIssue(principalContext(p), issue.ID)
	require.NoError(t, err)
	p.CanManageScope = true
	_, err = s.UpdateIssue(principalContext(p), issue.ID, IssueUpdate{Status: "closed"})
	require.Error(t, err, "aggregate does not authorize child writes")
	p.IsDirect = true
	without, err = s.ListIssues(principalContext(p), IssueFilter{})
	require.NoError(t, err)
	require.Zero(t, without.Total)
}
func TestWeeklyScheduleUsesRequestedTimezone(t *testing.T) {
	after := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC) // Sunday 09:00 Shanghai
	next, err := nextWeekly(ReportSchedule{Weekday: 1, Hour: 9, Minute: 30, Timezone: "Asia/Shanghai"}, after)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 21, 1, 30, 0, 0, time.UTC), next)
	_, err = nextWeekly(ReportSchedule{Weekday: 7, Timezone: "Asia/Shanghai"}, after)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = nextWeekly(ReportSchedule{Timezone: "not/a/timezone"}, after)
	require.ErrorIs(t, err, ErrInvalid)
}
func TestScheduleNativeTargetChecksAndDeduplicatedDelivery(t *testing.T) {
	s := testService(t)
	p := testPrincipal()
	native := principalContext(p)
	_, err := s.CreateIssue(native, gapInput())
	require.NoError(t, err)
	validations := 0
	s.ReportPrincipal = func(_ context.Context, scheduled ReportSchedule) (context.Context, error) {
		validations++
		if scheduled.ScopeID != "scope-A" || scheduled.RecipientType == "private" && scheduled.RecipientUID != "alice" {
			return nil, ErrDenied
		}
		return native, nil
	}
	delivered := 0
	s.ReportDelivery = func(_ context.Context, scheduled ReportSchedule, report *Report, file *ReportArtifact) error {
		delivered++
		require.NotEmpty(t, scheduled.RunKey)
		require.EqualValues(t, 1, report.Total)
		require.NotNil(t, file)
		require.Equal(t, "text/html; charset=utf-8", file.ContentType)
		return nil
	}
	input := ReportSchedule{Name: "每周知识问题", ChannelID: "native-channel", ScopeID: "scope-A", RecipientType: "private", RecipientUID: "bob", Weekday: 1, Hour: 9, Timezone: "Asia/Shanghai", Format: "html", Enabled: true}
	_, err = s.PutReportSchedule(consoleTestContext(), input)
	require.ErrorIs(t, err, ErrDenied)
	input.RecipientUID = "alice"
	row, err := s.PutReportSchedule(consoleTestContext(), input)
	require.NoError(t, err)
	due := time.Now().UTC().Add(time.Minute)
	require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumn("next_run_at", due).Error)
	queue := &capturingQueue{}
	require.NoError(t, s.EnqueueDueReports(context.Background(), queue, due.Add(time.Minute)))
	require.Len(t, queue.tasks, 1)
	require.NoError(t, s.ProcessReportTask(context.Background(), queue.tasks[0]))
	require.NoError(t, s.ProcessReportTask(context.Background(), queue.tasks[0]))
	require.Equal(t, 1, delivered)
	require.GreaterOrEqual(t, validations, 4)
	schedules, err := s.ListReportSchedules(consoleTestContext())
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	require.Equal(t, "sent", schedules[0].LastStatus)
}
func TestRevokedReportTargetFailsWithoutSendAndQueueFailureRetainsDue(t *testing.T) {
	s := testService(t)
	native := principalContext(testPrincipal())
	allowed := true
	s.ReportPrincipal = func(context.Context, ReportSchedule) (context.Context, error) {
		if !allowed {
			return nil, ErrDenied
		}
		return native, nil
	}
	sent := 0
	s.ReportDelivery = func(context.Context, ReportSchedule, *Report, *ReportArtifact) error { sent++; return nil }
	row, err := s.PutReportSchedule(consoleTestContext(), ReportSchedule{Name: "报告", ChannelID: "channel", ScopeID: "scope-A", RecipientType: "source", Weekday: 1, Hour: 9, Timezone: "Asia/Shanghai", Format: "text", Enabled: true})
	require.NoError(t, err)
	due := time.Now().UTC()
	require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumn("next_run_at", due).Error)
	queue := &capturingQueue{err: errors.New("queue unavailable")}
	require.NoError(t, s.EnqueueDueReports(context.Background(), queue, due.Add(time.Second)))
	var stored ReportSchedule
	require.NoError(t, s.db.First(&stored, "id = ?", row.ID).Error)
	require.WithinDuration(t, due, stored.NextRunAt, time.Second)
	require.Equal(t, "queue_failed", stored.LastStatus)
	queue.err = nil
	queue.tasks = nil
	require.NoError(t, s.EnqueueDueReports(context.Background(), queue, due.Add(time.Second)))
	require.Len(t, queue.tasks, 1)
	allowed = false
	require.Error(t, s.ProcessReportTask(context.Background(), queue.tasks[0]))
	require.Zero(t, sent)
	require.NoError(t, s.ProcessReportTask(context.Background(), queue.tasks[0]))
	require.Zero(t, sent)
	require.NoError(t, s.db.First(&stored, "id = ?", row.ID).Error)
	require.Equal(t, "failed", stored.LastStatus)
}
func TestReportFileToolSendsOnlyThroughTrustedSink(t *testing.T) {
	s := testService(t)
	ctx := principalContext(testPrincipal())
	result, err := NewTool(s).Execute(ctx, []byte(`{"operation":"report","report_format":"html"}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	calls := 0
	ctx = WithReportSink(ctx, func(context.Context, *ReportArtifact) error { calls++; return nil })
	result, err = NewTool(s).Execute(ctx, []byte(`{"operation":"report","report_format":"html"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, calls)
	require.NotContains(t, result.Output, "<!doctype")
	require.Contains(t, result.Output, `"delivered":true`)
}
