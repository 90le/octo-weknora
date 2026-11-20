package octobusiness

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const issueRetestLease = 2 * time.Minute

type retestRecord struct {
	ID                string     `gorm:"primaryKey" json:"id"`
	TenantID          uint64     `json:"-"`
	IssueID           string     `json:"-"`
	ActorUID          string     `json:"actor_uid"`
	QuestionMessageID string     `json:"question_message_id"`
	Scenario          string     `json:"scenario"`
	Question          string     `json:"question"`
	PlanRevision      int        `json:"plan_revision"`
	DocumentID        string     `json:"document_id"`
	SourceVersion     string     `json:"source_version"`
	DocumentCited     bool       `json:"document_cited"`
	State             string     `json:"state"`
	Answer            string     `json:"answer"`
	ModelID           string     `json:"model_id"`
	Sources           []string   `gorm:"serializer:json" json:"sources"`
	ErrorCode         string     `json:"error_code"`
	ReviewState       string     `json:"review_state"`
	ReviewNote        string     `json:"review_note"`
	ReviewerUID       string     `json:"reviewer_uid"`
	ReviewedAt        *time.Time `json:"reviewed_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

func (retestRecord) TableName() string { return "octo_issue_retests" }

func (s *Service) retestRows(ctx context.Context, issue *Issue) *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx).Model(&retestRecord{}).
		Where("tenant_id = ? AND issue_id = ?", issue.TenantID, issue.ID)
}

func (s *Service) retestAdminIssue(ctx context.Context, id string) (Principal, *Issue, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return p, nil, ErrDenied
	}
	issue, _, err := s.GetIssue(ctx, id)
	if err != nil {
		return p, nil, err
	}
	_, err = s.checkKB(ctx, p, issue.KnowledgeBaseID, false)
	return p, issue, err
}

func (s *Service) beginIssueRetest(
	ctx context.Context, p Principal, issue *Issue, questionID string, now time.Time, run *retestRecord,
) (*retestRecord, error) {
	run.ID, run.TenantID, run.IssueID, run.ActorUID = uuid.NewString(), p.TenantID, issue.ID, p.UserID
	run.State, run.CreatedAt, run.QuestionMessageID, run.Sources = "running", now, questionID, []string{}
	if err := expireIssueRetests(s.retestRows(ctx, issue), now); err != nil {
		return nil, err
	}
	insert := s.retestRows(ctx, issue).Clauses(clause.OnConflict{DoNothing: true}).Create(run)
	if insert.Error != nil {
		return nil, insert.Error
	}
	if insert.RowsAffected != 1 {
		return nil, ErrConflict
	}
	return run, nil
}

func expireIssueRetests(query *gorm.DB, now time.Time) error {
	return query.Where("state = 'running' AND created_at < ?", now.Add(-issueRetestLease)).
		Updates(map[string]any{"state": "interrupted", "finished_at": now, "error_code": "interrupted"}).Error
}

func (s *Service) finishIssueRetest(
	ctx context.Context, issue *Issue, run *retestRecord, result *issueRetest, runErr error,
) error {
	now := time.Now().UTC()
	run.FinishedAt, run.State = &now, "completed"
	if runErr == nil {
		run.Answer, run.ModelID, run.Sources = result.Answer, result.ModelID, result.Sources
		run.DocumentCited = run.DocumentID != "" && contains(result.citedDocuments, run.DocumentID)
	} else {
		run.State, run.ErrorCode = "failed", "execution"
		switch {
		case errors.Is(runErr, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
			run.State, run.ErrorCode = "interrupted", "interrupted"
		case errors.Is(runErr, errRetestUnavailable), errors.Is(runErr, errRemediationChanged):
			run.ErrorCode = "source_changed"
		}
	}
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	query := s.retestRows(write, issue).Where("id = ? AND state = 'running'", run.ID).
		Select("state", "answer", "model_id", "sources", "document_cited", "error_code", "finished_at").Updates(run)
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// IssueRetests lists previous attempts for the authorized issue.
func (h *Handler) IssueRetests(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	_, issue, err := h.service.retestAdminIssue(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	rows := []retestRecord{}
	err = h.service.retestRows(ctx, issue).Order("created_at DESC, id").Limit(30).Find(&rows).Error
	for i := range rows {
		if rows[i].State == "running" && time.Since(rows[i].CreatedAt) > issueRetestLease {
			rows[i].State, rows[i].ErrorCode = "interrupted", "interrupted"
		}
	}
	respond(c, rows, err)
}

type retestReview struct {
	State string `json:"state"`
	Note  string `json:"note"`
}

func (s *Service) reviewIssueRetest(
	ctx context.Context, p Principal, issue *Issue, id string, input retestReview,
) (*retestRecord, error) {
	if !contains([]string{"verified", "unresolved", "insufficient"}, input.State) || !validText(input.Note, 4000) {
		return nil, ErrInvalid
	}
	var run retestRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		query := tx.Model(&retestRecord{}).Where("tenant_id = ? AND issue_id = ? AND id = ?", p.TenantID, issue.ID, id)
		change := query.Where("state = 'completed' AND review_state = ''").Updates(map[string]any{
			"review_state": input.State, "review_note": strings.TrimSpace(input.Note),
			"reviewer_uid": p.UserID, "reviewed_at": now,
		})
		if change.Error != nil {
			return change.Error
		}
		err := tx.Where("tenant_id = ? AND issue_id = ? AND id = ?", p.TenantID, issue.ID, id).Take(&run).Error
		if err != nil {
			return err
		}
		if change.RowsAffected != 1 {
			if run.ReviewerUID == p.UserID && run.ReviewState == input.State &&
				run.ReviewNote == strings.TrimSpace(input.Note) {
				return nil
			}
			return ErrConflict
		}
		if input.State == "verified" && run.PlanRevision > 0 {
			if run.DocumentID != "" && !run.DocumentCited && run.Scenario != "counterexample" {
				return ErrConflict
			}
			store := *s
			store.db = tx
			if _, err := store.retestAgent(ctx, issue); err != nil {
				return err
			}
			if err := store.validateRunVersion(ctx, issue, &run); err != nil {
				return err
			}
		}
		return audit(tx, p, "octo.issue.retest_reviewed", issue.ID,
			map[string]string{"retest_id": id, "review_state": input.State})
	})
	return &run, err
}

func (s *Service) addReportRetests(
	ctx context.Context, issueIDs *gorm.DB, tenant uint64, from, to time.Time, metrics map[string]int64,
) error {
	for _, key := range []string{"completed", "failed", "verified", "unresolved", "insufficient"} {
		metrics["retest_"+key] = 0
	}
	base := s.db.WithContext(ctx).Model(&retestRecord{}).Where("tenant_id = ? AND issue_id IN (?)", tenant, issueIDs)
	var executions, reviews []struct {
		Key   string
		Count int64
	}
	if err := base.Session(&gorm.Session{}).Where("finished_at >= ? AND finished_at < ?", from, to).
		Select("state AS key, count(*) AS count").Group("state").Scan(&executions).Error; err != nil {
		return err
	}
	if err := base.Session(&gorm.Session{}).Where("reviewed_at >= ? AND reviewed_at < ?", from, to).
		Select("review_state AS key, count(*) AS count").Group("review_state").Scan(&reviews).Error; err != nil {
		return err
	}
	for _, run := range executions {
		if run.Key == "interrupted" {
			run.Key = "failed"
		}
		metrics["retest_"+run.Key] += run.Count
	}
	for _, review := range reviews {
		metrics["retest_"+review.Key] = review.Count
	}
	tested := base.Session(&gorm.Session{}).Where("plan_revision > 0 AND state = 'completed' AND finished_at >= ? AND finished_at < ?", from, to)
	var issuesTested, issuesNeedingWork int64
	if err := tested.Distinct("issue_id").Count(&issuesTested).Error; err != nil {
		return err
	}
	reviewed := base.Session(&gorm.Session{}).Where("plan_revision > 0 AND review_state IN ? AND reviewed_at >= ? AND reviewed_at < ?", []string{"unresolved", "insufficient"}, from, to)
	if err := reviewed.Distinct("issue_id").Count(&issuesNeedingWork).Error; err != nil {
		return err
	}
	metrics["repair_tested_issues"], metrics["repair_needing_work"] = issuesTested, issuesNeedingWork
	return nil
}

// ReviewIssueRetest saves the maintainer's conclusion without changing the issue status.
func (h *Handler) ReviewIssueRetest(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	p, issue, err := h.service.retestAdminIssue(ctx, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	var input retestReview
	if c.ShouldBindJSON(&input) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	run, err := h.service.reviewIssueRetest(ctx, p, issue, c.Param("run"), input)
	respond(c, run, err)
}
