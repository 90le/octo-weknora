package octobusiness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const IssueProposalAction = "register_issue"

type issueProposalPayload struct {
	Issue         Issue  `json:"issue"`
	QuotedReplyID string `json:"quoted_reply_id,omitempty"`
}

func (s *Service) ProposeIssue(ctx context.Context, in IssueInput) (*Proposal, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	issue, kbName, err := s.prepareIssue(ctx, p, in)
	if err != nil {
		return nil, err
	}
	preview := map[string]any{
		"action": IssueProposalAction, "knowledge_base_name": kbName, "scope": p.ScopeName,
		"title": issue.Title, "kind": issue.Kind, "description": issue.Description,
		"steps": issue.Steps, "expected": issue.Expected, "quoted_reply_id": p.QuotedReplyID,
		"notice": "确认后进入问题列表。",
	}
	payload := issueProposalPayload{Issue: *issue, QuotedReplyID: p.QuotedReplyID}
	proposal, err := s.saveProposal(ctx, p, IssueProposalAction, in.KnowledgeBaseID, payload, preview)
	if proposal != nil {
		proposal.Payload = nil
	}
	return proposal, err
}

func (s *Service) confirmIssue(ctx context.Context, p Principal, proposal *Proposal, cancel bool) error {
	if p.MessageID == "" || proposal.SourceMessageID == p.MessageID {
		return ErrDenied
	}
	if _, err := s.checkKB(ctx, p, proposal.KnowledgeBaseID, false); err != nil {
		return err
	}
	if proposal.Status == "completed" || proposal.Status == "cancelled" {
		return nil
	}
	var payload issueProposalPayload
	if json.Unmarshal(proposal.Payload, &payload) != nil ||
		payload.Issue.MessageID != proposal.SourceMessageID || payload.Issue.ReporterUID != p.UserID ||
		payload.Issue.KnowledgeBaseID != proposal.KnowledgeBaseID {
		return ErrInvalid
	}
	payload.Issue.TenantID = p.TenantID
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reserve the pending preview and write both records in the same transaction.
		now := time.Now()
		proposals := tx.Model(&Proposal{}).Where("id = ? AND tenant_id = ?", proposal.ID, p.TenantID)
		claimed := proposals.Session(&gorm.Session{}).Where("status = 'pending' AND expires_at > ?", now).
			UpdateColumns(map[string]any{"status": "executing", "updated_at": now})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected == 0 {
			found := Proposal{}
			if err := proposals.First(&found).Error; err != nil {
				return err
			}
			if found.Status == "completed" || found.Status == "cancelled" {
				*proposal = found
				return nil
			}
			return ErrConflict
		}
		next := "cancelled"
		result := proposal.Result
		if !cancel {
			if err := insertIssue(tx, p, &payload.Issue); err != nil {
				return err
			}
			if payload.QuotedReplyID != "" {
				event := IssueEvent{
					ID: uuid.NewString(), TenantID: p.TenantID, IssueID: payload.Issue.ID,
					ActorUID: p.UserID, ActorName: p.UserName, Status: "open",
					Note:           "反馈引用的 Octo 回复消息编号 " + payload.QuotedReplyID,
					IdempotencyKey: digest("issue-quoted-reply", payload.Issue.ID),
				}
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).Create(&event).Error; err != nil {
					return err
				}
			}
			encoded, err := json.Marshal(map[string]string{"issue_id": payload.Issue.ID, "title": payload.Issue.Title})
			if err != nil {
				return err
			}
			next, result = "completed", types.JSON(encoded)
		}
		if err := proposals.Updates(map[string]any{"status": next, "result": result, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := audit(tx, p, "octo.issue.proposal."+next, proposal.ID, map[string]string{"knowledge_base_id": proposal.KnowledgeBaseID}); err != nil {
			return err
		}
		proposal.Status, proposal.Result = next, result
		return nil
	})
}
