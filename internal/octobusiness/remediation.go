package octobusiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The console coordinator and the business contact on Issue belong to different identity domains.
type remediationRecord struct {
	IssueID          string          `gorm:"primaryKey" json:"-"`
	TenantID         uint64          `json:"-"`
	Coordinator      string          `json:"coordinator"`
	Revision         int             `json:"revision"`
	VerificationMode string          `gorm:"default:strict" json:"verification_mode"`
	Plan             remediationPlan `gorm:"serializer:json;type:text" json:"plan"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (remediationRecord) TableName() string { return "octo_issue_remediations" }

type remediationPlan struct {
	Cause          string        `json:"cause"`
	Acceptance     string        `json:"acceptance"`
	Paraphrase     string        `json:"paraphrase"`
	Counterexample string        `json:"counterexample"`
	WaitForUpdate  bool          `json:"wait_for_update"`
	Source         *repairSource `json:"source,omitempty"`
}

type repairSource struct {
	DocumentID string `json:"document_id"`
	Version    string `json:"version"`
	Path       string `json:"path"`
	DataSource string `json:"data_source"`
	ExternalID string `json:"external_id"`
}

func publishedSource(doc *types.Knowledge) *repairSource {
	if doc == nil || doc.Channel != "local_folder" || doc.ParseStatus != types.ParseStatusCompleted ||
		doc.ProcessedAt == nil || !types.IsPublishedKnowledgeForAnswer(doc) {
		return nil
	}
	var meta map[string]string
	if json.Unmarshal(doc.Metadata, &meta) != nil {
		return nil
	}
	for _, key := range []string{"datasource_id", "external_id", "source_version", "source_path"} {
		if meta[key] == "" {
			return nil
		}
	}
	return &repairSource{doc.ID, meta["source_version"], meta["source_path"], meta["datasource_id"], meta["external_id"]}
}

type remediationView struct {
	remediationRecord
	State     string        `json:"state"`
	Published *repairSource `json:"published,omitempty"`
}

func (s *Service) remediation(ctx context.Context, issue *Issue) (*remediationView, error) {
	view := &remediationView{State: "unclaimed"}
	view.VerificationMode = "lightweight"
	db := s.db.WithContext(ctx)
	found := db.Where("tenant_id = ? AND issue_id = ?", issue.TenantID, issue.ID).Limit(1).Find(&view.remediationRecord)
	if found.Error != nil || found.RowsAffected == 0 {
		return view, found.Error
	}
	if view.VerificationMode == "" {
		view.VerificationMode = "strict"
	}
	base := view.Plan.Source
	if base == nil {
		view.State = "ready"
		if view.Plan.Cause == "material" {
			view.State = "needs_source"
		}
		return view, nil
	}
	var doc types.Knowledge
	result := db.Where("tenant_id = ? AND knowledge_base_id = ? AND channel = ?", issue.TenantID, issue.KnowledgeBaseID, "local_folder").
		Where("metadata->>'datasource_id' = ? AND metadata->>'external_id' = ?", base.DataSource, base.ExternalID).Limit(1).Find(&doc)
	if result.Error != nil {
		return nil, result.Error
	}
	view.Published = publishedSource(&doc)
	view.State = "unavailable"
	if view.Published != nil {
		view.State = "ready"
		if view.Plan.WaitForUpdate && base.Version == view.Published.Version {
			view.State = "waiting"
		}
	}
	return view, nil
}

type remediationInput struct {
	Revision       int    `json:"revision"`
	Cause          string `json:"cause"`
	Acceptance     string `json:"acceptance"`
	Paraphrase     string `json:"paraphrase"`
	Counterexample string `json:"counterexample"`
	DocumentID     string `json:"document_id"`
	WaitForUpdate  bool   `json:"wait_for_update"`
}

func (s *Service) saveRemediation(ctx context.Context, p Principal, issue *Issue, in remediationInput) error {
	if !p.Console || issue.TenantID != p.TenantID {
		return ErrDenied
	}
	if !contains([]string{"material", "retrieval", "answer", "service", "permission"}, in.Cause) ||
		!validText(in.Acceptance, 4000) || !validText(in.Paraphrase, 2000) || !validText(in.Counterexample, 2000) ||
		strings.TrimSpace(in.Paraphrase) == strings.TrimSpace(in.Counterexample) || len(in.DocumentID) > 128 ||
		(in.WaitForUpdate && in.DocumentID == "") || in.Revision < 0 {
		return ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var active Issue
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ? AND archive_state = '' AND status IN ?", p.TenantID, issue.ID, []string{"open", "inprogress"}).First(&active).Error
		if err != nil {
			return err
		}
		var current remediationRecord
		q := tx.Where("tenant_id = ? AND issue_id = ?", p.TenantID, issue.ID).Limit(1).Find(&current)
		if q.Error != nil {
			return q.Error
		}
		if in.Revision != current.Revision {
			return ErrConflict
		}
		plan := remediationPlan{Cause: in.Cause, Acceptance: strings.TrimSpace(in.Acceptance), Paraphrase: strings.TrimSpace(in.Paraphrase),
			Counterexample: strings.TrimSpace(in.Counterexample), WaitForUpdate: in.WaitForUpdate}
		if in.DocumentID != "" {
			// Editing the wording must not replace the baseline with a newer publication.
			if current.Plan.Source != nil && in.DocumentID == current.Plan.Source.DocumentID {
				plan.Source = current.Plan.Source
			} else {
				var doc types.Knowledge
				if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?", p.TenantID, issue.KnowledgeBaseID, in.DocumentID).First(&doc).Error; err != nil {
					return err
				}
				plan.Source = publishedSource(&doc)
				if plan.Source == nil {
					return ErrInvalid
				}
			}
		}
		record := remediationRecord{IssueID: issue.ID, TenantID: p.TenantID, Coordinator: p.UserID, Revision: in.Revision + 1, VerificationMode: "strict", Plan: plan, UpdatedAt: time.Now().UTC()}
		if current.Revision == 0 {
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
		} else {
			record.Coordinator = current.Coordinator
			res := tx.Model(&record).Where("tenant_id = ? AND revision = ?", p.TenantID, in.Revision).Select("plan", "revision", "verification_mode", "updated_at").Updates(&record)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrConflict
			}
		}
		return audit(tx, p, "octo.issue.remediation_updated", issue.ID, map[string]string{"cause": in.Cause})
	})
}

// SetRemediationMode toggles an issue bi-directionally between lightweight
// (1-click instant retest & resolve) and strict (version gate + 3-case retest)
// while recording an immutable audit event and issue timeline entry.
func (h *Handler) SetRemediationMode(c *gin.Context) {
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
	var in struct {
		Mode   string `json:"mode"`
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil || !contains([]string{"lightweight", "strict"}, in.Mode) || len(in.Reason) > 1000 {
		respond(c, nil, ErrInvalid)
		return
	}
	if err = h.service.setRemediationMode(ctx, p, issue, in.Mode, strings.TrimSpace(in.Reason)); err != nil {
		respond(c, nil, err)
		return
	}
	view, err := h.service.remediation(ctx, issue)
	respond(c, view, err)
}

func (s *Service) setRemediationMode(ctx context.Context, p Principal, issue *Issue, mode, reason string) error {
	if !p.Console || issue.TenantID != p.TenantID {
		return ErrDenied
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current remediationRecord
		found := tx.Where("tenant_id = ? AND issue_id = ?", p.TenantID, issue.ID).Limit(1).Find(&current)
		if found.Error != nil {
			return found.Error
		}
		now := time.Now().UTC()
		if found.RowsAffected > 0 {
			if err := tx.Model(&remediationRecord{}).
				Where("tenant_id = ? AND issue_id = ?", p.TenantID, issue.ID).
				Updates(map[string]any{"verification_mode": mode, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		note := "已开启严格修订与三类回归门禁"
		if mode == "lightweight" {
			note = "已关闭严格门禁并退回轻量闭环模式"
		}
		if reason != "" {
			note += "：" + reason
		}
		ev := IssueEvent{
			ID:             uuid.NewString(),
			TenantID:       p.TenantID,
			IssueID:        issue.ID,
			ActorUID:       p.UserID,
			ActorName:      p.UserName,
			Status:         issue.Status,
			Note:           note,
			IdempotencyKey: uuid.NewString(),
		}
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		return audit(tx, p, "octo.issue.strict_mode_toggled", issue.ID, map[string]string{"mode": mode})
	})
}

func (h *Handler) IssueRemediation(c *gin.Context) {
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
	if c.Request.Method == "PUT" {
		var in remediationInput
		if c.ShouldBindJSON(&in) != nil {
			respond(c, nil, ErrInvalid)
			return
		}
		if err = h.service.saveRemediation(ctx, p, issue, in); err != nil {
			respond(c, nil, err)
			return
		}
	}
	view, err := h.service.remediation(ctx, issue)
	respond(c, view, err)
}

var errRemediationChanged = errors.New("maintenance plan or published source changed")

func (s *Service) requireVerifiedRepair(ctx context.Context, issue *Issue) error {
	v, err := s.remediation(ctx, issue)
	if err != nil || v.Revision == 0 || v.VerificationMode == "lightweight" {
		return err
	}
	if v.State != "ready" {
		return errRemediationChanged
	}
	if _, err = s.retestAgent(ctx, issue); err != nil {
		return err
	}
	docID, version := "", ""
	if v.Published != nil {
		docID, version = v.Published.DocumentID, v.Published.Version
	}
	for _, scenario := range []string{"original", "paraphrase", "counterexample"} {
		var last retestRecord
		result := s.retestRows(ctx, issue).Where("plan_revision = ? AND scenario = ? AND document_id = ? AND source_version = ?", v.Revision, scenario, docID, version).
			Order("created_at DESC, id DESC").Limit(1).Find(&last)
		if result.Error != nil {
			return result.Error
		}
		if last.State != "completed" || last.ReviewState != "verified" {
			return errRemediationChanged
		}
	}
	return nil
}

func (s *Service) prepareRun(ctx context.Context, issue *Issue, original, scenario string, revision int) (*retestRecord, error) {
	if scenario == "" {
		scenario = "original"
	}
	view, err := s.remediation(ctx, issue)
	if err != nil {
		return nil, err
	}
	if view.Revision != revision || (view.Revision > 0 && view.State != "ready") {
		return nil, errRemediationChanged
	}
	run := &retestRecord{Question: original, Scenario: scenario, PlanRevision: revision}
	switch scenario {
	case "original":
	case "paraphrase":
		run.Question = view.Plan.Paraphrase
	case "counterexample":
		run.Question = view.Plan.Counterexample
	default:
		return nil, ErrInvalid
	}
	if !validText(run.Question, 8000) {
		return nil, ErrInvalid
	}
	if view.Published != nil {
		run.DocumentID, run.SourceVersion = view.Published.DocumentID, view.Published.Version
	}
	return run, nil
}

func (s *Service) validateRunVersion(ctx context.Context, issue *Issue, run *retestRecord) error {
	current, err := s.prepareRun(ctx, issue, run.Question, run.Scenario, run.PlanRevision)
	if err != nil {
		return err
	}
	if current.DocumentID != run.DocumentID || current.SourceVersion != run.SourceVersion {
		return errRemediationChanged
	}
	return nil
}
