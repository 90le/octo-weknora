package octo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

// The status is derived only from the persisted proposal. Failed execution is
// deliberately described as uncertain because a side effect may have landed.
func confirmationTerminalText(p *octobusiness.Proposal, now time.Time) string {
	if p == nil {
		return ""
	}
	switch p.Status {
	case "completed":
		if p.Action == "delete_kb" {
			return deletionReceiptText
		}
		return "已完成，知识变更已执行。"
	case "cancelled":
		return "已取消，未执行本次变更。"
	case "failed":
		return "处理未确认完成，请先检查实际结果，再决定是否重新发起。"
	case "pending":
		// Waiting a minute avoids racing a confirmation accepted just before expiry.
		if now.After(p.ExpiresAt.Add(time.Minute)) {
			return "已过期，未执行本次变更。请重新发起预览。"
		}
	}
	return ""
}

func (a *Adapter) runConfirmationCardReconciliation(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		a.reconcileConfirmationCards(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Only cards emitted by this version are edited. Older cards remain untouched;
// pending cards and their existing typed-command fallback stay rollback-safe.
func (a *Adapter) reconcileConfirmationCards(ctx context.Context) {
	if a == nil || a.db == nil || a.business == nil || ctx.Err() != nil {
		return
	}
	var cards []confirmationCard
	err := a.db.WithContext(ctx).Table("octo_card_bindings AS c").Select("c.*").
		Joins("JOIN octo_knowledge_proposals AS p ON p.id = c.proposal_id "+
			"AND p.tenant_id = c.tenant_id AND p.channel_id = c.channel_id").
		Where("c.tenant_id = ? AND c.channel_id = ? AND c.state = ? AND c.client_msg_no LIKE ? "+
			"AND c.created_at > ? AND (c.updated_at < p.updated_at OR c.updated_at < ?) AND "+
			"(p.status IN ? OR (p.status = ? AND p.expires_at < ?))",
			a.tenantID, a.channelID, "sent", "c04d0002-%", time.Now().Add(-24*time.Hour),
			time.Now().Add(-30*time.Second), []string{"completed", "cancelled", "failed"},
			"pending", time.Now().Add(-time.Minute)).Order("c.updated_at").Limit(10).Scan(&cards).Error
	if err != nil {
		return
	}
	for _, card := range cards {
		if ctx.Err() != nil {
			return
		}
		if card.ClientMsgNo != confirmationCardClientID(card.ProposalID) {
			continue
		}
		// Throttle failures and authorization changes without changing state.
		r := a.db.WithContext(ctx).Model(&confirmationCard{}).
			Where("tenant_id = ? AND channel_id = ? AND proposal_id = ? AND state = ? AND updated_at = ?",
				a.tenantID, a.channelID, card.ProposalID, "sent", card.UpdatedAt).
			Update("updated_at", time.Now())
		if r.Error != nil || r.RowsAffected != 1 {
			continue
		}
		a.finishConfirmationCard(ctx, card)
	}
}

func (a *Adapter) finishConfirmationCard(ctx context.Context, card confirmationCard) {
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var proposal octobusiness.Proposal
	if a.db.WithContext(callCtx).Where("tenant_id = ? AND channel_id = ? AND id = ?",
		a.tenantID, a.channelID, card.ProposalID).First(&proposal).Error != nil {
		return
	}
	text := confirmationTerminalText(&proposal, time.Now())
	if text == "" {
		return
	}
	var source Inbox
	if a.inboxQuery(callCtx, proposal.SourceMessageID).First(&source).Error != nil {
		return
	}
	var msg im.IncomingMessage
	if json.Unmarshal([]byte(source.Input), &msg) != nil || msg.MessageID != proposal.SourceMessageID ||
		msg.UserID != proposal.UserID {
		return
	}
	scope, err := a.AuthorizeExecution(callCtx, nil, &msg)
	allowed := err == nil && scope != nil && scope.AccountID == proposal.AccountID &&
		scope.ScopeID == proposal.ScopeID && permitsReply(source.Authority, scope)
	if !allowed && proposal.Action == "delete_kb" && proposal.Status == "completed" {
		// The existing deletion receipt independently verifies the tombstone,
		// current native member and scope even if deletion removed the last KB.
		receipt := msg
		receipt.MessageID = "octo:card-receipt:" + proposal.ID
		receipt.Extra = make(map[string]string, len(msg.Extra)+1)
		for key, value := range msg.Extra {
			receipt.Extra[key] = value
		}
		receipt.Extra["octo_command_text"] = "确认 " + proposal.ID
		allowed = a.deletionReceipt(callCtx, &receipt) != nil
	}
	if !allowed {
		return
	}
	target, kind, ok := a.typingTarget(&msg)
	if !ok {
		return
	}
	body := []any{
		map[string]any{"type": "TextBlock", "text": "知识变更", "weight": "Bolder", "wrap": true},
		map[string]any{"type": "TextBlock", "text": text, "wrap": true},
	}
	// Cancellation and expiry retain the original preview as context. A
	// completed proposal may instead contain an execution result, so include it
	// only when the same strict preview validation succeeds.
	if preview, ok := confirmationPreview(&proposal); ok {
		body = append(body, confirmationPreviewBlocks(preview)...)
	}
	cardBody := map[string]any{
		"type": "AdaptiveCard", "version": "1.5",
		"body": body,
	}
	payload, err := json.Marshal(map[string]any{
		"type": 17, "profile": "octo/v1", "card_version": "1.5", "card": cardBody,
	})
	if err != nil {
		return
	}
	if a.api.post(callCtx, "/v1/bot/message/edit", map[string]any{
		"message_id": card.MessageID, "channel_id": target, "channel_type": kind, "content_edit": string(payload),
	}, nil) != nil {
		return
	}
	_ = a.db.WithContext(callCtx).Model(&confirmationCard{}).
		Where("tenant_id = ? AND channel_id = ? AND proposal_id = ? AND state = ?",
			a.tenantID, a.channelID, card.ProposalID, "sent").
		Updates(map[string]any{"state": "closed", "updated_at": time.Now()}).Error
}
