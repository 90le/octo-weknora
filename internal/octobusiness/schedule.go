package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const TypeReportSend = "octo:report_send"
const reportTaskTimeout = 2 * time.Minute
const reportRecoveryGrace = time.Minute

var errReportScheduleChanged = errors.New("report schedule changed or disabled")

type ReportSchedule struct {
	ID            string     `json:"id" gorm:"primaryKey"`
	TenantID      uint64     `json:"-"`
	Name          string     `json:"name"`
	ChannelID     string     `json:"channel_id"`
	ScopeID       string     `json:"scope_id"`
	RecipientType string     `json:"recipient_type"`
	RecipientUID  string     `json:"recipient_uid"`
	Weekday       int        `json:"weekday"`
	Hour          int        `json:"hour"`
	Minute        int        `json:"minute"`
	Timezone      string     `json:"timezone"`
	Format        string     `json:"format"`
	Enabled       bool       `json:"enabled"`
	CreatedBy     string     `json:"created_by"`
	NextRunAt     time.Time  `json:"next_run_at"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastStatus    string     `json:"last_status"`
	LastError     string     `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	RunKey        string     `json:"-" gorm:"-"`
}

func (ReportSchedule) TableName() string { return "octo_report_schedules" }

type ReportRun struct {
	ID         string `gorm:"primaryKey"`
	TenantID   uint64
	ScheduleID string
	DueAt      time.Time
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (ReportRun) TableName() string { return "octo_report_runs" }

type reportTask struct {
	TenantID   uint64    `json:"tenant_id"`
	ScheduleID string    `json:"schedule_id"`
	DueAt      time.Time `json:"due_at"`
	Revision   time.Time `json:"revision"`
}

func nextWeekly(s ReportSchedule, after time.Time) (time.Time, error) {
	if s.Weekday < 0 || s.Weekday > 6 || s.Hour < 0 || s.Hour > 23 || s.Minute < 0 || s.Minute > 59 {
		return time.Time{}, ErrInvalid
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	local := after.In(loc)
	for offset := 0; offset <= 7; offset++ {
		day := local.AddDate(0, 0, offset)
		candidate := time.Date(day.Year(), day.Month(), day.Day(), s.Hour, s.Minute, 0, 0, loc)
		if int(candidate.Weekday()) == s.Weekday && candidate.After(after) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, ErrInvalid
}
func (s *Service) ListReportSchedules(ctx context.Context) ([]ReportSchedule, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	rows := []ReportSchedule{}
	err = s.db.WithContext(ctx).Where("tenant_id = ?", p.TenantID).Order("name, id").Limit(200).Find(&rows).Error
	return rows, err
}
func (s *Service) PutReportSchedule(ctx context.Context, input ReportSchedule) (*ReportSchedule, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if !validText(input.Name, 256) || !validText(input.ChannelID, 128) || !validText(input.ScopeID, 128) {
		return nil, ErrInvalid
	}
	if input.RecipientType != "source" && input.RecipientType != "private" {
		return nil, ErrInvalid
	}
	if input.RecipientType == "private" && !validText(input.RecipientUID, 128) {
		return nil, ErrInvalid
	}
	if input.RecipientType == "source" {
		input.RecipientUID = ""
	}
	if input.Format != "text" && input.Format != "markdown" && input.Format != "html" && input.Format != "csv" {
		return nil, ErrInvalid
	}
	if input.Timezone == "" {
		input.Timezone = "Asia/Shanghai"
	}
	next, err := nextWeekly(input, time.Now())
	if err != nil {
		return nil, err
	}
	input.TenantID = p.TenantID
	input.NextRunAt = next
	if input.Enabled {
		if s.ReportPrincipal == nil {
			return nil, ErrDenied
		}
		checked, e := s.ReportPrincipal(ctx, input)
		if e != nil {
			return nil, ErrDenied
		}
		if _, e = currentPrincipal(checked); e != nil {
			return nil, e
		}
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
		input.CreatedBy = p.UserID
		input.CreatedAt = time.Now()
		input.LastRunAt = nil
		input.LastStatus = ""
		input.LastError = ""
	} else {
		var old ReportSchedule
		if err = s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, input.ID).First(&old).Error; err != nil {
			return nil, err
		}
		input.CreatedBy = old.CreatedBy
		input.CreatedAt = old.CreatedAt
		input.LastRunAt = old.LastRunAt
		input.LastStatus = old.LastStatus
		input.LastError = old.LastError
	}
	input.UpdatedAt = time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Save(&input).Error; e != nil {
			return e
		}
		return audit(tx, p, "octo.report.configured", input.ID, map[string]any{"enabled": input.Enabled, "scope_id": input.ScopeID, "recipient_type": input.RecipientType})
	})
	return &input, err
}
func (s *Service) DeleteReportSchedule(ctx context.Context, id string) error {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return ErrDenied
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Where("tenant_id = ? AND id = ?", p.TenantID, id).Delete(&ReportSchedule{})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return audit(tx, p, "octo.report.deleted", id, nil)
	})
}

// StartReports reuses the native cron and task-queue libraries in this process.
// Persistent due times and deterministic queue IDs prevent multi-instance sends.
func (s *Service) StartReports(ctx context.Context, queue interfaces.TaskEnqueuer) error {
	if queue == nil || s.reportCron != nil {
		return ErrInvalid
	}
	scheduler := cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger), cron.SkipIfStillRunning(cron.DefaultLogger)))
	if _, err := scheduler.AddFunc("* * * * *", func() {
		scanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.EnqueueDueReports(scanCtx, queue, time.Now())
	}); err != nil {
		return err
	}
	s.reportCron = scheduler
	scheduler.Start()
	return nil
}
func (s *Service) StopReports() {
	if s.reportCron != nil {
		stopped := s.reportCron.Stop()
		<-stopped.Done()
	}
}
func (s *Service) EnqueueDueReports(ctx context.Context, queue interfaces.TaskEnqueuer, now time.Time) error {
	if err := s.recoverInterruptedReports(ctx, now); err != nil {
		return err
	}
	var schedules []ReportSchedule
	if err := s.db.WithContext(ctx).Where("enabled = ? AND next_run_at <= ?", true, now).Order("next_run_at, id").Limit(100).Find(&schedules).Error; err != nil {
		return err
	}
	for _, schedule := range schedules {
		payload, err := json.Marshal(reportTask{TenantID: schedule.TenantID, ScheduleID: schedule.ID, DueAt: schedule.NextRunAt, Revision: schedule.UpdatedAt})
		if err != nil {
			return err
		}
		key := "octoreport:" + schedule.ID + ":" + schedule.NextRunAt.UTC().Format("200601021504")
		_, err = queue.Enqueue(asynq.NewTask(TypeReportSend, payload), asynq.TaskID(key), asynq.Queue(types.QueueSync), asynq.MaxRetry(0), asynq.Timeout(reportTaskTimeout))
		if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
			_ = s.db.WithContext(ctx).Model(&ReportSchedule{}).Where("id = ? AND updated_at = ?", schedule.ID, schedule.UpdatedAt).UpdateColumns(map[string]any{"last_status": "queue_failed", "last_error": "Unable to enqueue report; the scheduler will retry"}).Error
			continue
		}
		next, err := nextWeekly(schedule, now)
		if err != nil {
			return err
		}
		if err = s.db.WithContext(ctx).Model(&ReportSchedule{}).Where("id = ? AND next_run_at = ? AND updated_at = ?", schedule.ID, schedule.NextRunAt, schedule.UpdatedAt).UpdateColumns(map[string]any{"next_run_at": next}).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) ProcessReportTask(ctx context.Context, task *asynq.Task) error {
	// The native in-process queue does not apply asynq timeout options.
	// Bound the handler itself so interrupted-run recovery has the same deadline
	// in Redis and non-Redis deployments.
	ctx, cancel := context.WithTimeout(ctx, reportTaskTimeout)
	defer cancel()
	var input reportTask
	if json.Unmarshal(task.Payload(), &input) != nil || input.TenantID == 0 || input.ScheduleID == "" {
		return ErrInvalid
	}
	var schedule ReportSchedule
	err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND enabled = ?", input.ScheduleID, input.TenantID, true).First(&schedule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !schedule.UpdatedAt.Equal(input.Revision) {
		return nil
	}
	key := digest("report", fmtTenant(schedule.TenantID), schedule.ID, input.DueAt.UTC().Format(time.RFC3339Nano))
	schedule.RunKey = key
	run := ReportRun{ID: key, TenantID: schedule.TenantID, ScheduleID: schedule.ID, DueAt: input.DueAt, Status: "sending"}
	claim := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil
	}
	now := time.Now().UTC()
	_ = s.db.WithContext(ctx).Model(&ReportSchedule{}).Where("id = ?", schedule.ID).UpdateColumns(map[string]any{"last_status": "sending", "last_error": "", "last_run_at": now}).Error
	sendErr := func() error {
		if s.ReportPrincipal == nil || s.ReportDelivery == nil {
			return ErrDenied
		}
		reportCtx, e := s.ReportPrincipal(ctx, schedule)
		if e != nil {
			return ErrDenied
		}
		reportCtx, e = s.guardReportSchedule(reportCtx, schedule)
		if e != nil {
			return e
		}
		originalPrincipal, e := currentPrincipal(reportCtx)
		if e != nil {
			return ErrDenied
		}
		loc, e := time.LoadLocation(schedule.Timezone)
		if e != nil {
			return ErrInvalid
		}
		from := input.DueAt.In(loc).AddDate(0, 0, -7).UTC()
		to := input.DueAt
		report, e := s.Report(reportCtx, IssueFilter{From: &from, To: &to})
		if e != nil {
			return e
		}
		var file *ReportArtifact
		if schedule.Format != "text" {
			file, e = ReportFile(report, schedule.Format)
			if e != nil {
				return e
			}
		}
		// Check the active channel/bindings/recipient immediately before delivery too.
		freshCtx, e := s.ReportPrincipal(ctx, schedule)
		if e != nil {
			return ErrDenied
		}
		freshPrincipal, e := currentPrincipal(freshCtx)
		if e != nil || reportAuthority(originalPrincipal) != reportAuthority(freshPrincipal) {
			return ErrDenied
		}
		if e = s.ensureReportScheduleCurrent(ctx, schedule); e != nil {
			return e
		}
		return s.ReportDelivery(WithReportAuthority(reportCtx, originalPrincipal), schedule, report, file)
	}()
	status := "sent"
	message := ""
	if sendErr != nil {
		status = "failed"
		message = "Report was not confirmed delivered. Check the channel, scope and recipient before sending again."
		if errors.Is(sendErr, errReportScheduleChanged) {
			message = "Report cancelled before delivery because its schedule was changed or disabled."
		}
	}
	if err = s.db.WithContext(ctx).Model(&ReportRun{}).Where("id = ?", key).Updates(map[string]any{"status": status, "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	if err = s.db.WithContext(ctx).Model(&ReportSchedule{}).Where("id = ? AND tenant_id = ?", schedule.ID, schedule.TenantID).UpdateColumns(map[string]any{"last_status": status, "last_error": message, "last_run_at": now}).Error; err != nil {
		return err
	}
	if sendErr != nil {
		return fmt.Errorf("scheduled report delivery failed: %w", ErrDenied)
	}
	return nil
}

func reportAuthority(p Principal) string {
	ids := append([]string(nil), p.KnowledgeBaseIDs...)
	ids = append(ids, p.ReadIssueKnowledgeBaseIDs...)
	sort.Strings(ids)
	scopes := append([]string(nil), p.ReadIssueScopeIDs...)
	sort.Strings(scopes)
	return digest(fmtTenant(p.TenantID), p.AccountID, p.ChannelID, p.ScopeID, p.UserID, fmt.Sprint(p.IsDirect), strings.Join(ids, ","), strings.Join(scopes, ","))
}

// Recheck the exact persisted schedule as well as channel/knowledge permissions.
// Do not silently deliver a report prepared for an older recipient or settings.
func (s *Service) ensureReportScheduleCurrent(ctx context.Context, schedule ReportSchedule) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&ReportSchedule{}).Where("id = ? AND tenant_id = ? AND enabled = ? AND updated_at = ?", schedule.ID, schedule.TenantID, true, schedule.UpdatedAt).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errReportScheduleChanged
	}
	return nil
}

// Attach the revision check to the same principal used by report-authority
// validation, so file sinks repeat it after upload and before the send request.
func (s *Service) guardReportSchedule(ctx context.Context, schedule ReportSchedule) (context.Context, error) {
	if err := s.ensureReportScheduleCurrent(ctx, schedule); err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, ErrDenied
	}
	originalValidation := principal.Validate
	original := principal
	principal.Validate = func(callCtx context.Context) (Principal, error) {
		if err := s.ensureReportScheduleCurrent(callCtx, schedule); err != nil {
			return Principal{}, err
		}
		if originalValidation != nil {
			return originalValidation(callCtx)
		}
		return original, nil
	}
	return WithPrincipal(ctx, principal), nil
}

// A worker may stop after reserving a send but before recording its outcome.
// Keep its idempotency claim, never resend automatically, and surface uncertainty
// after the task deadline plus a grace period. This is safe across replicas.
func (s *Service) recoverInterruptedReports(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-reportTaskTimeout - reportRecoveryGrace)
	var runs []ReportRun
	if err := s.db.WithContext(ctx).Where("status = ? AND updated_at < ?", "sending", cutoff).Order("updated_at, id").Limit(100).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			claimed := tx.Model(&ReportRun{}).Where("id = ? AND tenant_id = ? AND status = ? AND updated_at < ?", run.ID, run.TenantID, "sending", cutoff).Updates(map[string]any{"status": "uncertain", "updated_at": now})
			if claimed.Error != nil {
				return claimed.Error
			}
			if claimed.RowsAffected == 0 {
				return nil
			}
			var latest ReportRun
			if err := tx.Where("tenant_id = ? AND schedule_id = ?", run.TenantID, run.ScheduleID).Order("due_at DESC, created_at DESC, id DESC").First(&latest).Error; err != nil {
				return err
			}
			if latest.ID != run.ID {
				return nil
			}
			return tx.Model(&ReportSchedule{}).Where("id = ? AND tenant_id = ?", run.ScheduleID, run.TenantID).UpdateColumns(map[string]any{"last_status": "failed", "last_error": "上次报告发送被中断，是否送达尚不确定；系统不会自动重发。请先核对目标会话，再决定是否手动发送。"}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
