package octobusiness

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type issueBatchTarget struct {
	ID        string    `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
}

type issueBatchInput struct {
	Targets   []issueBatchTarget `json:"targets"`
	Action    string             `json:"action"`
	Status    string             `json:"status"`
	ContactID string             `json:"contact_id"`
	Note      string             `json:"note"`
}

type operationResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func operationState(err error) string {
	switch {
	case err == nil:
		return "updated"
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, ErrDenied):
		return "unavailable"
	case errors.Is(err, ErrConflict):
		return "changed"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	default:
		return "failed"
	}
}

func (s *Service) batchIssues(ctx context.Context, in issueBatchInput) ([]operationResult, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if !contains([]string{"status", "assign", "archive", "trash", "restore"}, in.Action) ||
		len(in.Targets) == 0 || len(in.Targets) > 100 || len(in.Note) > 4000 ||
		in.Action == "assign" && !validText(in.ContactID, 128) {
		return nil, ErrInvalid
	}
	targets := map[string]bool{}
	for _, item := range in.Targets {
		if !validText(item.ID, 128) || item.UpdatedAt.IsZero() || targets[item.ID] {
			return nil, ErrInvalid
		}
		targets[item.ID] = true
	}
	result := make([]operationResult, 0, len(in.Targets))
	for _, item := range in.Targets {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			copy := *s
			copy.db = tx
			return copy.applyIssueBatch(ctx, p, item, in)
		})
		result = append(result, operationResult{ID: item.ID, State: operationState(err)})
	}
	return result, nil
}

func (s *Service) applyIssueBatch(
	ctx context.Context, p Principal, item issueBatchTarget, in issueBatchInput,
) error {
	var row Issue
	q := s.issueQuery(ctx, p, IssueFilter{Visibility: "detail"})
	if err := q.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", item.ID).Take(&row).Error; err != nil {
		return err
	}
	if _, err := s.checkKB(ctx, p, row.KnowledgeBaseID, false); err != nil {
		return err
	}
	if !row.UpdatedAt.Equal(item.UpdatedAt) {
		return ErrConflict
	}
	switch in.Action {
	case "assign", "status":
		next := IssueUpdate{Status: row.Status, Note: in.Note}
		if in.Action == "assign" {
			next.OwnerContactID = in.ContactID
		} else {
			next.Status = in.Status
		}
		_, err := s.UpdateIssue(ctx, row.ID, next)
		return err
	case "archive":
		if row.ArchiveState != "" {
			return ErrConflict
		}
		row.ArchiveState = "archived"
	case "trash":
		row.ArchiveState = "trash"
	case "restore":
		row.ArchiveState = ""
	}
	if err := s.db.Model(&row).Update("archive_state", row.ArchiveState).Error; err != nil {
		return err
	}
	event := IssueEvent{
		ID: uuid.NewString(), TenantID: p.TenantID, IssueID: row.ID, ActorUID: p.UserID,
		ActorName: p.UserName, Status: row.Status, Note: in.Action + " " + in.Note, IdempotencyKey: uuid.NewString(),
	}
	if err := s.db.Create(&event).Error; err != nil {
		return err
	}
	return audit(s.db, p, types.AuditAction("octo.issue."+in.Action), row.ID, map[string]string{"archive_state": row.ArchiveState})
}

// BatchIssues applies checked operations to the selected issue versions.
func (h *Handler) BatchIssues(c *gin.Context) {
	ctx, err := ConsoleContext(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var in issueBatchInput
	if c.ShouldBindJSON(&in) != nil {
		respond(c, nil, ErrInvalid)
		return
	}
	result, err := h.service.batchIssues(ctx, in)
	respond(c, result, err)
}
