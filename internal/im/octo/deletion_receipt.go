package octo

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

const deletionReceiptText = "删除请求已提交，后台清理中"

// A completed native deletion may legitimately remove the last KB grant. Only
// this fixed receipt can cross that boundary; no Agent/body/tool text is reused.
// The proposal is durable proof and current native connection/member checks
// still apply when replaying a pending delivery after a process restart.
func (a *Adapter) deletionReceipt(ctx context.Context, msg *im.IncomingMessage) *im.ExecutionScope {
	if a.db == nil || a.receiptPolicy == nil || msg == nil || msg.Platform != Platform || msg.ChatType == im.ChatTypeDirect || msg.MessageID == "" || msg.UserID == "" {
		return nil
	}
	text, ok := msg.Extra["octo_command_text"]
	if !ok {
		return nil
	} // Must originate from the native normalizer, not model text.
	id, cancelled, recognized := octobusiness.Confirmation(text)
	if !recognized || cancelled {
		return nil
	}
	var proposal octobusiness.Proposal
	if a.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND channel_id = ? AND user_id = ? AND action = ? AND status = ?", id, a.tenantID, a.channelID, msg.UserID, "delete_kb", "completed").First(&proposal).Error != nil || proposal.SourceMessageID == msg.MessageID || proposal.KnowledgeBaseID == "" {
		return nil
	}
	var input octobusiness.ManagementInput
	var result struct {
		KnowledgeBaseID   string `json:"knowledge_base_id"`
		DeletionRequested bool   `json:"deletion_requested"`
	}
	if json.Unmarshal(proposal.Payload, &input) != nil || input.Action != "delete_kb" || input.KnowledgeBaseID != proposal.KnowledgeBaseID || json.Unmarshal(proposal.Result, &result) != nil || !result.DeletionRequested || result.KnowledgeBaseID != proposal.KnowledgeBaseID {
		return nil
	}
	var tombstone int64
	if a.db.WithContext(ctx).Table("knowledge_bases").Where("tenant_id = ? AND id = ? AND deleted_at IS NOT NULL", a.tenantID, proposal.KnowledgeBaseID).Count(&tombstone).Error != nil || tombstone != 1 {
		return nil
	}
	scope, err := a.receiptPolicy(ctx, nil, msg)
	if err != nil || scope == nil || !scope.CanManageScope || scope.AccountID != proposal.AccountID || scope.ScopeID != proposal.ScopeID {
		return nil
	}
	return scope
}
