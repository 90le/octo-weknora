package octobusiness

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ManagementInput struct {
	Action          string `json:"action"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Content         string `json:"content,omitempty"`
	ScopeID         string `json:"scope_id,omitempty"`
	// ExpectedRevision is captured by preview, never a tool argument.
	ExpectedRevision string `json:"expected_revision,omitempty"`
}

// ManagementExecutor extends the same preview/confirm lifecycle using native
// service methods. dryRun=true must have no side effects; both phases recheck
// current native permissions. Return a concise impact preview when dry-running.
type ManagementExecutor func(context.Context, Principal, ManagementInput, bool) (map[string]any, error)

func fmtTenant(tenant uint64) string { return strconv.FormatUint(tenant, 10) }

func (s *Service) management(ctx context.Context, p Principal, in ManagementInput, preview bool) (map[string]any, error) {
	if len(in.Content) > 200000 || len(in.Name) > 300 {
		return nil, ErrInvalid
	}
	if in.Action != "rename" && in.Action != "save_draft" && in.Action != "publish" && in.Action != "delete_document" {
		if s.Management == nil {
			return nil, ErrInvalid
		}
		return s.Management(ctx, p, in, preview)
	}
	kb, err := s.checkKB(ctx, p, in.KnowledgeBaseID, true)
	if err != nil {
		return nil, err
	}
	if in.Action == "rename" && !validText(in.Name, 300) {
		return nil, ErrInvalid
	}
	if in.Action == "save_draft" && (!validText(in.Name, 300) || !validText(in.Content, 200000)) {
		return nil, ErrClarify
	}
	var knowledge *types.Knowledge
	if in.KnowledgeID != "" {
		if s.knowledge == nil {
			return nil, ErrInvalid
		}
		knowledge, err = s.knowledge.GetKnowledgeByID(types.WithExecutionTenant(ctx, p.TenantID), in.KnowledgeID)
		if err != nil {
			return nil, err
		}
		if knowledge.TenantID != p.TenantID || knowledge.KnowledgeBaseID != kb.ID {
			return nil, ErrDenied
		}
	}
	if (in.Action == "publish" || in.Action == "delete_document") && knowledge == nil {
		return nil, ErrInvalid
	}
	revision := kb.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if knowledge != nil {
		revision = knowledge.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if in.ExpectedRevision != "" && in.ExpectedRevision != revision {
		return nil, ErrConflict
	}
	if in.Action == "publish" && (!validText(in.Name, 300) || !validText(in.Content, 200000)) {
		return nil, ErrInvalid
	}
	if preview {
		return map[string]any{"action": in.Action, "knowledge_base_id": kb.ID, "knowledge_base_name": kb.Name, "knowledge_id": in.KnowledgeID, "name": in.Name, "content": in.Content, "revision": revision, "notice": "Saving a draft does not publish it; deleting a document does not delete its original source."}, nil
	}
	// The explicit checked scope grants only this KB to the existing service.
	caller := types.CallerFromContext(ctx)
	if caller.TenantID != p.TenantID {
		return nil, ErrDenied
	}
	grant, err := access.ResolveKB(ctx, access.KBRequest{Caller: caller}, kb, types.OrgRoleEditor, nil, nil)
	if err != nil {
		return nil, err
	}
	ctx = grant.Context(ctx)
	if in.Action == "rename" {
		if s.kb == nil {
			return nil, ErrInvalid
		}
		updated, e := s.kb.UpdateKnowledgeBase(ctx, kb.ID, in.Name, kb.Description, nil)
		if e != nil {
			return nil, e
		}
		return map[string]any{"knowledge_base_id": updated.ID, "name": updated.Name}, nil
	}
	if s.knowledge == nil {
		return nil, ErrInvalid
	}
	if in.Action == "delete_document" {
		err = s.knowledge.DeleteKnowledge(ctx, in.KnowledgeID)
		return map[string]any{"knowledge_id": in.KnowledgeID, "deleted": err == nil}, err
	}
	status := types.ManualKnowledgeStatusDraft
	if in.Action == "publish" {
		status = types.ManualKnowledgeStatusPublish
	}
	payload := &types.ManualKnowledgePayload{Title: in.Name, Content: in.Content, Status: status, Channel: "octo"}
	var saved *types.Knowledge
	if in.KnowledgeID != "" {
		saved, err = s.knowledge.UpdateManualKnowledge(ctx, in.KnowledgeID, payload)
	} else {
		saved, err = s.knowledge.CreateKnowledgeFromManual(ctx, kb.ID, payload, "octo")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"knowledge_id": saved.ID, "title": in.Name, "status": status}, nil
}

func (s *Service) Propose(ctx context.Context, in ManagementInput) (*Proposal, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.MessageID == "" {
		return nil, ErrDenied
	}
	preview, err := s.management(ctx, p, in, true)
	if err != nil {
		return nil, err
	}
	if revision, ok := preview["revision"].(string); ok {
		in.ExpectedRevision = revision
	}
	payload, _ := json.Marshal(in)
	result, _ := json.Marshal(preview)
	key := digest("proposal", fmtTenant(p.TenantID), p.AccountID, p.ChannelID, p.ScopeID, p.UserID, p.MessageID, string(payload))
	row := Proposal{ID: "OP-" + uuid.NewString(), TenantID: p.TenantID, AccountID: p.AccountID, ChannelID: p.ChannelID, ScopeID: p.ScopeID, UserID: p.UserID, Action: in.Action, KnowledgeBaseID: in.KnowledgeBaseID, Payload: types.JSON(payload), Result: types.JSON(result), Status: "pending", SourceMessageID: p.MessageID, IdempotencyKey: key, ExpiresAt: time.Now().Add(15 * time.Minute)}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).Create(&row)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			row = Proposal{}
			return tx.Where("idempotency_key = ?", key).First(&row).Error
		}
		return audit(tx, p, "octo.knowledge.proposed", row.ID, map[string]string{"action": in.Action, "knowledge_base_id": in.KnowledgeBaseID})
	})
	return &row, err
}

var confirmationRE = regexp.MustCompile(`(?i)^(确认|确认执行|确认保存|confirm|取消|取消操作|cancel)\s+(OP-[0-9a-f-]{36})[。.!！]?$`)

// Confirmation parses transport-origin message text, never model arguments.
// A native @ prefix should be removed by the adapter before setting MessageText.
func Confirmation(text string) (id string, cancel bool, ok bool) {
	m := confirmationRE.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) != 3 {
		return "", false, false
	}
	return m[2], strings.HasPrefix(m[1], "取消") || strings.EqualFold(m[1], "cancel"), true
}

func (s *Service) Confirm(ctx context.Context, id string, cancel bool) (*Proposal, error) {
	p, err := currentPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Console {
		nativeID, nativeCancel, ok := Confirmation(p.MessageText)
		if !ok || nativeID != id || nativeCancel != cancel {
			return nil, ErrDenied
		}
	}
	var row Proposal
	q := s.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND channel_id = ? AND scope_id = ? AND user_id = ? AND id = ?", p.TenantID, p.AccountID, p.ChannelID, p.ScopeID, p.UserID, id)
	if err = q.First(&row).Error; err != nil {
		return nil, err
	}
	if row.Status == "completed" || row.Status == "cancelled" {
		return &row, nil
	}
	if row.Status != "pending" || time.Now().After(row.ExpiresAt) {
		return nil, ErrConflict
	}
	if !p.Console && row.SourceMessageID == p.MessageID {
		return nil, ErrDenied
	}
	var in ManagementInput
	if json.Unmarshal(row.Payload, &in) != nil {
		return nil, ErrInvalid
	}
	// Fresh role and exact asset authorization is checked before reserving.
	if !cancel {
		if _, err = s.management(ctx, p, in, true); err != nil {
			return nil, err
		}
	}
	next := "executing"
	if cancel {
		next = "cancelled"
	}
	r := s.db.WithContext(ctx).Model(&Proposal{}).Where("tenant_id = ? AND account_id = ? AND channel_id = ? AND scope_id = ? AND user_id = ? AND id = ? AND status = 'pending' AND expires_at > ?", p.TenantID, p.AccountID, p.ChannelID, p.ScopeID, p.UserID, id, time.Now()).Updates(map[string]any{"status": next, "updated_at": time.Now()})
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected != 1 {
		return nil, ErrConflict
	}
	row.Status = next
	if cancel {
		return &row, nil
	}
	result, err := s.management(ctx, p, in, false)
	if err != nil {
		// Do not replay uncertain side effects after process/network failures.
		_ = s.db.WithContext(ctx).Model(&Proposal{}).Where("id = ? AND status = 'executing'", id).Updates(map[string]any{"status": "failed", "updated_at": time.Now()}).Error
		return nil, err
	}
	encoded, _ := json.Marshal(result)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&Proposal{}).Where("id = ? AND tenant_id = ? AND status = 'executing'", id, p.TenantID).Updates(map[string]any{"status": "completed", "result": types.JSON(encoded), "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		return audit(tx, p, "octo.knowledge.executed", id, map[string]string{"action": in.Action, "knowledge_base_id": in.KnowledgeBaseID})
	})
	row.Status = "completed"
	row.Result = types.JSON(encoded)
	return &row, err
}
