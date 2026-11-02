package octo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// The event only identifies a stored binding. Neither the operation nor its
// target is ever taken from data supplied by a card or a language model.
func (a *Adapter) acceptConfirmationEvent(ctx context.Context, event storedCardEvent, handler func(context.Context, *im.IncomingMessage) error) (bool, error) {
	var binding confirmationCard
	err := a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND message_id = ? AND state = ?", a.tenantID, a.channelID, event.MessageID, "sent").First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var proposal octobusiness.Proposal
	if err = a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND id = ?", a.tenantID, a.channelID, binding.ProposalID).First(&proposal).Error; err != nil {
		return false, err
	}
	var source Inbox
	if err = a.inboxQuery(ctx, proposal.SourceMessageID).First(&source).Error; err != nil {
		return false, err
	}
	var msg im.IncomingMessage
	if json.Unmarshal([]byte(source.Input), &msg) != nil || msg.MessageID != proposal.SourceMessageID || msg.UserID != proposal.UserID {
		return false, errors.New("invalid original confirmation context")
	}
	target, kind, ok := a.typingTarget(&msg)
	if !ok || event.OperatorUID != proposal.UserID || event.TargetID != target || event.TargetType != kind ||
		(event.ActionID != "knowledge_confirm" && event.ActionID != "knowledge_cancel") {
		return true, a.setCardEventState(ctx, event.EventKey, "processed")
	}
	scope, err := a.AuthorizeExecution(ctx, nil, &msg)
	if err != nil || scope == nil || scope.AccountID != proposal.AccountID || scope.ScopeID != proposal.ScopeID || !permitsReply(source.Authority, scope) {
		return true, a.setCardEventState(ctx, event.EventKey, "processed")
	}
	msg.MessageID = fmt.Sprintf("octo:%s:card:%s", a.uid, event.EventKey)
	msg.MessageType = im.MessageTypeText
	msg.Content = "确认 " + proposal.ID
	if event.ActionID == "knowledge_cancel" {
		msg.Content = "取消 " + proposal.ID
	}
	msg.Quote = nil
	msg.FileKey, msg.FileName, msg.FileSize = "", "", 0
	msg.Extra["octo_card_event_key"] = event.EventKey
	msg.Extra["octo_message_id"] = event.MessageID
	msg.Extra["octo_command_text"] = msg.Content
	delete(msg.Extra, "octo_original_payload")
	if err := a.accept(ctx, &msg, handler); err != nil {
		// This direct handler returns errors only before Confirm starts. Keep
		// pre-execution transient failures eligible for a durable retry.
		if !errors.Is(err, im.ErrScopeDenied) {
			_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Where("state = ? AND error_code = ?", "failed", "dispatch_failed").Updates(map[string]any{"state": "queued", "error_code": "", "updated_at": time.Now()}).Error
		}
		return false, err
	}
	var saved Inbox
	err = a.inboxQuery(ctx, msg.MessageID).First(&saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, a.setCardEventState(ctx, event.EventKey, "processed")
	}
	if err != nil {
		return false, err
	}
	if saved.State == "queued" {
		if err := a.dispatch(ctx, &msg, handler); err != nil {
			if !errors.Is(err, im.ErrScopeDenied) {
				_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Where("state = ? AND error_code = ?", "failed", "dispatch_failed").Updates(map[string]any{"state": "queued", "error_code": "", "updated_at": time.Now()}).Error
			}
			return false, err
		}
	}
	return true, a.setCardEventState(ctx, event.EventKey, "processed")
}

func (a *Adapter) setCardEventState(ctx context.Context, key string, state string) error {
	return a.db.WithContext(ctx).Model(&storedCardEvent{}).Where("tenant_id = ? AND channel_id = ? AND event_key = ?", a.tenantID, a.channelID, key).Updates(map[string]any{"state": state, "updated_at": time.Now()}).Error
}

// Recovery invokes the same handler: only a matching durable event may enter
// the direct path; ordinary messages continue through the existing dispatcher.
func (a *Adapter) confirmationHandler(next func(context.Context, *im.IncomingMessage) error) func(context.Context, *im.IncomingMessage) error {
	return func(ctx context.Context, msg *im.IncomingMessage) error {
		key := msg.Extra["octo_card_event_key"]
		if key == "" {
			return next(ctx, msg)
		}
		if len(key) != 64 || msg.MessageID != fmt.Sprintf("octo:%s:card:%s", a.uid, key) {
			return im.ErrScopeDenied
		}
		var event storedCardEvent
		if a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND event_key = ?", a.tenantID, a.channelID, key).First(&event).Error != nil || cardEventKey(cardAction{EventID: event.EventID, MessageID: event.MessageID, ChannelID: event.TargetID, ChannelType: byte(event.TargetType), OperatorUID: event.OperatorUID, ActionID: event.ActionID}) != key {
			return im.ErrScopeDenied
		}
		var binding confirmationCard
		if a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND message_id = ? AND state = ?", a.tenantID, a.channelID, event.MessageID, "sent").First(&binding).Error != nil {
			return im.ErrScopeDenied
		}
		var proposal octobusiness.Proposal
		if a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND id = ? AND user_id = ?", a.tenantID, a.channelID, binding.ProposalID, event.OperatorUID).First(&proposal).Error != nil {
			return im.ErrScopeDenied
		}
		target, kind, ok := a.typingTarget(msg)
		if !ok || target != event.TargetID || kind != event.TargetType || msg.UserID != event.OperatorUID || msg.Extra["octo_message_id"] != event.MessageID {
			return im.ErrScopeDenied
		}
		cancel := event.ActionID == "knowledge_cancel"
		if !cancel && event.ActionID != "knowledge_confirm" {
			return im.ErrScopeDenied
		}
		command := "确认 " + proposal.ID
		if cancel {
			command = "取消 " + proposal.ID
		}
		if msg.Content != command {
			return im.ErrScopeDenied
		}
		makePrincipal := func(scope *im.ExecutionScope) octobusiness.Principal {
			return octobusiness.Principal{TenantID: a.tenantID, AccountID: scope.AccountID, ChannelID: a.channelID, ScopeID: scope.ScopeID, ScopeName: scope.ScopeName, GroupID: msg.Extra["octo_group_id"], SubareaID: msg.Extra["octo_subarea_id"], UserID: msg.UserID, UserName: scope.SenderName, MessageID: msg.MessageID, MessageText: command, IsDirect: msg.ChatType == im.ChatTypeDirect, KnowledgeBaseIDs: scope.KnowledgeBaseIDs, ManageKnowledgeBaseIDs: scope.ManageKnowledgeBaseIDs, CanManageScope: scope.CanManageScope, CanCreateKnowledgeBase: scope.AllowKnowledgeCreation, ReadIssueScopeIDs: scope.ReadIssueScopeIDs, ReadIssueKnowledgeBaseIDs: scope.ReadIssueKnowledgeBaseIDs}
		}
		scope, err := a.AuthorizeExecution(ctx, nil, msg)
		if err != nil || scope == nil || scope.AccountID != proposal.AccountID || scope.ScopeID != proposal.ScopeID {
			return im.ErrScopeDenied
		}
		var saved Inbox
		if a.inboxQuery(ctx, msg.MessageID).First(&saved).Error != nil || !permitsReply(saved.Authority, scope) {
			return im.ErrScopeDenied
		}
		p := makePrincipal(scope)
		p.Validate = func(callCtx context.Context) (octobusiness.Principal, error) {
			fresh, err := a.AuthorizeExecution(callCtx, nil, msg)
			if err != nil || fresh == nil || !permitsReply(saved.Authority, fresh) {
				return octobusiness.Principal{}, im.ErrScopeDenied
			}
			return makePrincipal(fresh), nil
		}
		// Match the native IM path: storage and parser services depend on the
		// current tenant configuration, not just the numeric tenant ID.
		var tenant types.Tenant
		if err := a.db.WithContext(ctx).Where("id = ?", a.tenantID).First(&tenant).Error; err != nil {
			return err
		}
		ctx = context.WithValue(ctx, types.TenantIDContextKey, a.tenantID)
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, &tenant)
		ctx = context.WithValue(ctx, types.LanguageContextKey, types.DefaultLanguage())
		ctx = context.WithValue(ctx, types.UserIDContextKey, fmt.Sprintf("system-%d", a.tenantID))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
		ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: fmt.Sprintf("%d:%s:%s:%s", a.tenantID, a.channelID, msg.Platform, msg.UserID)})
		ctx = types.WithIMKnowledgeScope(ctx, scope.KnowledgeBaseIDs)
		result, err := a.business.Confirm(octobusiness.WithPrincipal(ctx, p), proposal.ID, cancel)
		text := "操作未确认完成，状态可能未知。请先检查知识库和操作记录，再决定是否重新发起。"
		if err == nil && result != nil {
			if result.Status == "completed" {
				text = "知识变更已执行。"
			} else if result.Status == "cancelled" {
				text = "知识变更已取消。"
			}
		}
		// SendReply persists its result before attempting delivery. A transport
		// error must not cause the mutation to run again.
		_ = a.SendReply(ctx, msg, &im.ReplyMessage{Content: text, IsFinal: true})
		return nil
	}
}
