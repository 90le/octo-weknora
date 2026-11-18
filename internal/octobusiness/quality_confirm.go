package octobusiness

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const qualityAction = "review_quality"

type qualityConfirmation struct {
	Title     string `json:"title"`
	Note      string `json:"note"`
	ContactID string `json:"contact_id"`
}

func qualityAuthor(p Principal, scope *qualityScope, row qualityRow) Principal {
	p.AccountID, p.ChannelID, p.ScopeID = scope.AccountID, scope.ChannelID, scope.ID
	p.ScopeName, p.GroupID, p.SubareaID = scope.DisplayName, scope.GroupID, scope.SubareaID
	p.MessageID = row.ID
	return p
}

func (s *Service) confirmQuality(
	ctx context.Context, id string, input qualityConfirmation,
) (*Issue, error) {
	p, err := currentPrincipal(ctx)
	if err != nil || !p.Console {
		return nil, ErrDenied
	}
	if !validText(id, 128) || !validText(input.Title, 300) ||
		!validText(input.Note, 4000) || len(input.ContactID) > 128 {
		return nil, ErrClarify
	}
	find := func(db *gorm.DB) *gorm.DB {
		return db.Where("tenant_id = ? AND user_id = ? AND action = ? AND id = ?",
			p.TenantID, p.UserID, qualityAction, id)
	}
	var stored Proposal
	if err := find(s.db.WithContext(ctx)).First(&stored).Error; err != nil {
		return nil, err
	}
	var proposal qualityProposal
	if json.Unmarshal(stored.Payload, &proposal) != nil {
		return nil, ErrInvalid
	}
	scope, err := s.qualityScope(ctx, proposal.Input.ScopeID)
	if err != nil || scope.ID != stored.ScopeID || scope.AccountID != stored.AccountID ||
		scope.ChannelID != stored.ChannelID || stored.KnowledgeBaseID != proposal.Input.LibraryID ||
		!scope.permits(stored.KnowledgeBaseID) {
		return nil, ErrConflict
	}
	row := &Issue{}
	reviewDB := s.db.Session(&gorm.Session{Logger: gormlogger.Discard}).WithContext(ctx)
	err = reviewDB.Transaction(func(tx *gorm.DB) error {
		snapshot := *s
		snapshot.db = tx
		var locked Proposal
		if err := find(tx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked).Error; err != nil {
			return err
		}
		if locked.Status == "completed" {
			var result struct{ IssueID string }
			if json.Unmarshal(locked.Result, &result) != nil || result.IssueID == "" {
				return ErrConflict
			}
			return tx.Where("tenant_id = ? AND id = ?", p.TenantID, result.IssueID).First(row).Error
		}
		if locked.Status != "pending" || !time.Now().Before(locked.ExpiresAt) {
			return ErrConflict
		}
		rows, err := snapshot.loadQualityRows(ctx, scope, proposal.Input)
		if err != nil {
			return err
		}
		if rows[0].ID != locked.SourceMessageID {
			return ErrInvalid
		}
		excerpts, err := snapshot.qualityMaterials(ctx, scope, locked.KnowledgeBaseID, rows)
		if err != nil {
			return err
		}
		if qualitySignature(excerpts) != proposal.Signature {
			return ErrConflict
		}
		row, err = createReviewedIssue(ctx, &snapshot, p, scope, locked, input, proposal.Finding, rows[0])
		if err != nil {
			return err
		}
		for _, evidence := range rows {
			review := qualityReview{
				TenantID: p.TenantID, ChannelID: scope.ChannelID, ScopeID: scope.ID,
				MessageID: evidence.ID, IssueID: row.ID, UpdatedAt: time.Now().UTC(),
			}
			if err := snapshot.writeQualityReview(ctx, review, []string{"issue_id", "updated_at"}); err != nil {
				return err
			}
		}
		result, _ := json.Marshal(struct{ IssueID string }{row.ID})
		return tx.Model(&locked).Updates(map[string]any{
			"status": "completed", "result": string(result), "updated_at": time.Now(),
		}).Error
	})
	return row, err
}

func createReviewedIssue(
	ctx context.Context, s *Service, p Principal, scope *qualityScope, proposal Proposal,
	input qualityConfirmation, finding qualityFinding, source qualityRow,
) (*Issue, error) {
	kind := "suggestion"
	switch finding.Category {
	case "missing":
		kind = "missing"
	case "technical", "citation":
		kind = "bug"
	}
	notes := []string{strings.TrimSpace(input.Note), "AI 复核建议", finding.Suggestion}
	for _, item := range finding.Evidence {
		notes = append(notes, item.Ref+" "+item.Quote)
	}
	author := qualityAuthor(p, scope, source)
	issue := &Issue{
		TenantID: p.TenantID, ChannelID: scope.ChannelID, AccountID: scope.AccountID,
		ScopeID: scope.ID, ScopeName: scope.DisplayName, GroupID: scope.GroupID, SubareaID: scope.SubareaID,
		KnowledgeBaseID: proposal.KnowledgeBaseID, Kind: kind, Title: strings.TrimSpace(input.Title),
		Description: limitQualityText(strings.Join(notes, "\n"), 3900), Expected: finding.Suggestion, Status: "open",
		ReporterUID: source.userID, ReporterName: source.name, MessageID: source.ID, OriginalMessage: source.Question,
		Attachments: types.JSON("[]"),
	}
	issue.IdempotencyKey = qualityIssueKey(issue)
	if input.ContactID != "" {
		var contact Contact
		err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND id = ?",
			p.TenantID, issue.KnowledgeBaseID, input.ContactID).First(&contact).Error
		if err != nil {
			return nil, err
		}
		issue.OwnerUID, issue.OwnerName = contact.UID, contact.Name
	}
	if err := insertIssue(s.db.WithContext(ctx), author, issue); err != nil {
		return nil, err
	}
	event := IssueEvent{
		ID: uuid.NewString(), TenantID: p.TenantID, IssueID: issue.ID, ActorUID: p.UserID, ActorName: p.UserName,
		Status: issue.Status, Note: "人工确认问答质量分析后登记", IdempotencyKey: digest(qualityAction, proposal.ID),
	}
	return issue, s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error
}

func qualityIssueKey(issue *Issue) string {
	return digest(qualityAction, fmtTenant(issue.TenantID), issue.ChannelID, issue.ScopeID,
		issue.KnowledgeBaseID, issue.MessageID, issue.Kind)
}
