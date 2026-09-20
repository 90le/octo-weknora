package octobusiness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func reportScheduleFixture(t *testing.T, s *Service) *ReportSchedule {
	t.Helper()
	row, err := s.PutReportSchedule(consoleTestContext(), ReportSchedule{Name: "每周报告", ChannelID: "channel", ScopeID: "scope-A", RecipientType: "source", Weekday: 1, Hour: 9, Timezone: "Asia/Shanghai", Format: "html", Enabled: true})
	require.NoError(t, err)
	return row
}
func dueReportTask(t *testing.T, s *Service, row *ReportSchedule) *asynq.Task {
	t.Helper()
	due := time.Now().UTC()
	require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumn("next_run_at", due).Error)
	queue := &capturingQueue{}
	require.NoError(t, s.EnqueueDueReports(context.Background(), queue, due.Add(time.Second)))
	require.Len(t, queue.tasks, 1)
	return queue.tasks[0]
}
func TestScheduledReportRechecksRevisionAndEnabledBeforeDelivery(t *testing.T) {
	for _, change := range []string{"disabled", "recipient_changed", "deleted"} {
		t.Run(change, func(t *testing.T) {
			s := testService(t)
			native := principalContext(testPrincipal())
			var saved *ReportSchedule
			calls := 0
			s.ReportPrincipal = func(context.Context, ReportSchedule) (context.Context, error) {
				if saved != nil {
					calls++
					if calls == 2 {
						switch change {
						case "disabled":
							require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", saved.ID).UpdateColumn("enabled", false).Error)
						case "recipient_changed":
							require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", saved.ID).Updates(map[string]any{"recipient_type": "private", "recipient_uid": "different-user", "updated_at": time.Now().Add(time.Second)}).Error)
						case "deleted":
							require.NoError(t, s.db.Where("id = ?", saved.ID).Delete(&ReportSchedule{}).Error)
						}
					}
				}
				return native, nil
			}
			sends := 0
			s.ReportDelivery = func(context.Context, ReportSchedule, *Report, *ReportArtifact) error { sends++; return nil }
			saved = reportScheduleFixture(t, s)
			err := s.ProcessReportTask(context.Background(), dueReportTask(t, s, saved))
			require.Error(t, err)
			require.Zero(t, sends)
			var run ReportRun
			require.NoError(t, s.db.First(&run, "schedule_id = ?", saved.ID).Error)
			require.Equal(t, "failed", run.Status)
		})
	}
}
func TestScheduledReportRevisionGuardSurvivesFileUpload(t *testing.T) {
	s := testService(t)
	native := principalContext(testPrincipal())
	s.ReportPrincipal = func(context.Context, ReportSchedule) (context.Context, error) { return native, nil }
	sent := 0
	s.ReportDelivery = func(ctx context.Context, row ReportSchedule, _ *Report, file *ReportArtifact) error {
		require.NotNil(t, file)
		require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumn("enabled", false).Error)
		// Generated-file delivery invokes this immediately after upload and before send.
		if err := ValidateReportAuthority(ctx); err != nil {
			return err
		}
		sent++
		return nil
	}
	row := reportScheduleFixture(t, s)
	require.Error(t, s.ProcessReportTask(context.Background(), dueReportTask(t, s, row)))
	require.Zero(t, sent)
}
func TestInterruptedReportRunBecomesVisibleWithoutResendingAfterRestart(t *testing.T) {
	s := testService(t)
	native := principalContext(testPrincipal())
	s.ReportPrincipal = func(context.Context, ReportSchedule) (context.Context, error) { return native, nil }
	row := reportScheduleFixture(t, s)
	now := time.Now().UTC()
	old := now.Add(-reportTaskTimeout - reportRecoveryGrace - time.Minute)
	runID := digest("report", fmtTenant(row.TenantID), row.ID, old.Format(time.RFC3339Nano))
	require.NoError(t, s.db.Create(&ReportRun{ID: runID, TenantID: row.TenantID, ScheduleID: row.ID, DueAt: old, Status: "sending", CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumns(map[string]any{"last_status": "sending", "last_run_at": old}).Error)
	restarted := NewService(s.db, nil, nil)
	restarted.ReportPrincipal = s.ReportPrincipal
	sent := 0
	restarted.ReportDelivery = func(context.Context, ReportSchedule, *Report, *ReportArtifact) error { sent++; return nil }
	queue := &capturingQueue{}
	require.NoError(t, restarted.EnqueueDueReports(context.Background(), queue, now))
	require.Empty(t, queue.tasks)
	var run ReportRun
	require.NoError(t, s.db.First(&run, "id = ?", runID).Error)
	require.Equal(t, "uncertain", run.Status)
	var saved ReportSchedule
	require.NoError(t, s.db.First(&saved, "id = ?", row.ID).Error)
	require.Equal(t, "failed", saved.LastStatus)
	require.Contains(t, saved.LastError, "不会自动重发")
	payload, err := json.Marshal(reportTask{TenantID: row.TenantID, ScheduleID: row.ID, DueAt: old, Revision: row.UpdatedAt})
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessReportTask(context.Background(), asynq.NewTask(TypeReportSend, payload)))
	require.Zero(t, sent, "the existing claim must remain, even if a queue replays the interrupted task")
}
func TestReportRecoveryDoesNotOverwriteNewerOutcomeOrActiveSend(t *testing.T) {
	s := testService(t)
	native := principalContext(testPrincipal())
	s.ReportPrincipal = func(context.Context, ReportSchedule) (context.Context, error) { return native, nil }
	row := reportScheduleFixture(t, s)
	now := time.Now().UTC()
	old := now.Add(-10 * time.Minute)
	require.NoError(t, s.db.Create(&ReportRun{ID: "old-run", TenantID: row.TenantID, ScheduleID: row.ID, DueAt: old, Status: "sending", CreatedAt: old, UpdatedAt: old}).Error)
	require.NoError(t, s.db.Create(&ReportRun{ID: "new-run", TenantID: row.TenantID, ScheduleID: row.ID, DueAt: now, Status: "sending", CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, s.db.Model(&ReportSchedule{}).Where("id = ?", row.ID).UpdateColumns(map[string]any{"last_status": "sending", "last_run_at": now}).Error)
	require.NoError(t, s.recoverInterruptedReports(context.Background(), now))
	var current ReportRun
	require.NoError(t, s.db.First(&current, "id = ?", "new-run").Error)
	require.Equal(t, "sending", current.Status)
	var saved ReportSchedule
	require.NoError(t, s.db.First(&saved, "id = ?", row.ID).Error)
	require.Equal(t, "sending", saved.LastStatus)
}
