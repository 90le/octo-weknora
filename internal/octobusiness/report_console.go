package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type reportDates struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

// PreviewReportSchedule checks the source, output target and time window.
func (h *Handler) PreviewReportSchedule(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var request struct {
		ReportSchedule
		reportDates
	}
	if c.ShouldBindJSON(&request) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	now := time.Now().UTC()
	plan := request.ReportSchedule
	next, err := normalizeReportSchedule(&plan, now)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, _ := currentPrincipal(ctx)
	plan.TenantID = p.TenantID
	due := next
	if due.IsZero() {
		due = now
	}
	from, to, err := h.service.reportPeriod(ctx, plan, due, request.From, request.To)
	if err != nil {
		respond(c, nil, err)
		return
	}
	_, err = h.service.reportContext(ctx, plan.validationSource())
	respond(c, gin.H{"next": next, "from": from, "to": to, "authorized": err == nil, "code": reportErrorCode(err)}, nil)
}

func (s *Service) reportContext(ctx context.Context, plan ReportSchedule) (context.Context, error) {
	if s.ReportPrincipal == nil {
		return nil, ReportAccessError("channel")
	}
	return s.ReportPrincipal(ctx, plan)
}

func (s *Service) consolePlan(ctx context.Context, id string) (ReportSchedule, error) {
	var plan ReportSchedule
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return plan, ErrDenied
	}
	err = s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", p.TenantID, id).Take(&plan).Error
	return plan, err
}

// ReportHistory returns statuses and time windows of the retained runs.
func (h *Handler) ReportHistory(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	plan, err := h.service.consolePlan(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows := []ReportRun{}
	err = h.service.db.WithContext(ctx).Omit("snapshot").
		Where("tenant_id = ? AND schedule_id = ?", plan.TenantID, plan.ID).
		Order("due_at DESC, id").Limit(100).Find(&rows).Error
	respond(c, rows, err)
}

// GenerateReport creates a report snapshot without sending it.
func (h *Handler) GenerateReport(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	plan, err := h.service.consolePlan(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input reportDates
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	run, err := h.service.generateConsoleReport(ctx, plan, input)
	respond(c, run, err)
}

func (s *Service) generateConsoleReport(
	ctx context.Context, plan ReportSchedule, input reportDates,
) (*ReportRun, error) {
	ctx, cancel := context.WithTimeout(ctx, reportTaskTimeout)
	defer cancel()
	plan.Manual = true
	reportCtx, err := s.reportContext(ctx, plan.validationSource())
	if err != nil {
		return nil, err
	}
	reportCtx, err = s.guardReportSchedule(reportCtx, plan)
	if err != nil {
		return nil, err
	}
	p, err := currentPrincipal(reportCtx)
	if err != nil {
		return nil, err
	}
	due := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	from, to, err := s.reportPeriod(ctx, plan, due, input.From, input.To)
	if err != nil {
		return nil, err
	}
	id := digest("manual-report", fmtTenant(plan.TenantID), plan.ID, plan.UpdatedAt.UTC().Format(time.RFC3339Nano),
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	run := ReportRun{
		ID: id, TenantID: plan.TenantID, ScheduleID: plan.ID, DueAt: due, Status: "generating",
		Revision: &plan.UpdatedAt, FromAt: &from, ToAt: &to,
	}
	claim := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
	if claim.Error != nil {
		return nil, claim.Error
	}
	if claim.RowsAffected != 1 {
		if err := s.reportRunQuery(ctx, plan, id).Take(&run).Error; err != nil {
			return nil, err
		}
		// Failed generation performs no delivery and can be reclaimed. A
		// retained snapshot or an uncertain delivery keeps its original claim.
		retry := s.reportRunQuery(ctx, plan, id).
			Where("status = 'failed' AND snapshot = ''").
			Updates(map[string]any{"status": "generating", "error_code": "", "due_at": due})
		if retry.Error != nil || retry.RowsAffected != 1 {
			return &run, retry.Error
		}
		run.Status, run.ErrorCode, run.DueAt = "generating", "", due
	}
	report, processErr := s.prepareReport(reportCtx, p, plan, &run, input.From, input.To)
	run.Status = "generated"
	if plan.Mode != "generate" {
		run.Status = "pending"
	}
	if processErr == nil {
		if file, fileErr := ReportFile(report, "markdown"); fileErr == nil {
			run.Summary = file.Content
		}
	} else {
		run.Status, run.ErrorCode = "failed", reportErrorCode(processErr)
		if run.ErrorCode == "delivery_unconfirmed" {
			run.ErrorCode = "generation_failed"
		}
	}
	write, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelWrite()
	err = s.reportRunQuery(write, plan, id).
		Updates(map[string]any{"status": run.Status, "error_code": run.ErrorCode}).Error
	return &run, err
}

// ReportRunDetail shows the snapshot after rechecking current authorization.
func (h *Handler) ReportRunDetail(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	plan, err := h.service.consolePlan(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	run, _, err := h.service.loadReportRun(ctx, plan, c.Param("run"))
	respond(c, run, err)
}

func (s *Service) reportRunQuery(ctx context.Context, plan ReportSchedule, id string) *gorm.DB {
	return s.db.WithContext(ctx).Model(&ReportRun{}).
		Where("tenant_id = ? AND schedule_id = ? AND id = ?", plan.TenantID, plan.ID, id)
}

func (s *Service) loadReportRun(
	ctx context.Context, plan ReportSchedule, id string,
) (*ReportRun, context.Context, error) {
	var run ReportRun
	err := s.reportRunQuery(ctx, plan, id).Take(&run).Error
	if err != nil || run.Snapshot == "" {
		return &run, nil, err
	}
	reportCtx, err := s.reportContext(ctx, plan.validationSource())
	if err != nil {
		return nil, nil, err
	}
	p, err := currentPrincipal(reportCtx)
	if err != nil || run.Authority != reportAuthority(p) {
		return nil, nil, ErrDenied
	}
	var report Report
	if json.Unmarshal([]byte(run.Snapshot), &report) != nil {
		return nil, nil, ErrConflict
	}
	file, err := ReportFile(&report, "markdown")
	if err != nil {
		return nil, nil, err
	}
	run.Summary = file.Content
	return &run, WithReportAuthority(reportCtx, p), nil
}

// SendReportRun approves delivery of the retained snapshot to its verified destination.
func (h *Handler) SendReportRun(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	plan, err := h.service.consolePlan(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	out, err := h.service.sendReportRun(ctx, plan, c.Param("run"))
	respond(c, out, err)
}

func (s *Service) sendReportRun(ctx context.Context, plan ReportSchedule, id string) (*ReportRun, error) {
	ctx, cancel := context.WithTimeout(ctx, reportTaskTimeout)
	defer cancel()
	plan.Manual = true
	if plan.Mode == "generate" || s.ReportDelivery == nil {
		return nil, ErrDenied
	}
	run, reportCtx, err := s.loadReportRun(ctx, plan, id)
	if err != nil {
		return nil, err
	}
	if run.Status == "sent" || run.Status == "uncertain" {
		return run, nil
	}
	if run.Status != "pending" || run.Revision == nil || !run.Revision.Equal(plan.UpdatedAt) || reportCtx == nil {
		return nil, ErrConflict
	}
	var report Report
	if json.Unmarshal([]byte(run.Snapshot), &report) != nil {
		return nil, ErrConflict
	}
	var file *ReportArtifact
	if plan.Format != "text" {
		file, err = ReportFile(&report, plan.Format)
		if err != nil {
			return nil, err
		}
	}
	reportCtx, err = s.guardReportSchedule(reportCtx, plan)
	if err != nil {
		return nil, err
	}
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&ReportRun{}).
			Where("tenant_id = ? AND schedule_id = ? AND id = ?", plan.TenantID, plan.ID, id).
			Where("status = 'pending'").
			Update("status", "sending")
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrConflict
		}
		return audit(tx, p, "octo.report.approved", plan.ID, map[string]string{"run_id": id})
	})
	if err != nil {
		return nil, err
	}
	plan.RunKey = id
	sendErr := s.ReportDelivery(reportCtx, plan, &report, file)
	run.Status, run.ErrorCode = "sent", reportErrorCode(sendErr)
	if sendErr != nil {
		run.Status = "uncertain"
		if errors.Is(sendErr, ErrDenied) || errors.Is(sendErr, errReportScheduleChanged) {
			run.Status = "failed"
		}
	}
	write, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelWrite()
	err = s.reportRunQuery(write, plan, id).
		Updates(map[string]any{"status": run.Status, "error_code": run.ErrorCode}).Error
	return run, err
}
