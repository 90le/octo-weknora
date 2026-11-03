package octo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const fileDeliveryGuardKey = "octo_file_delivery_guard_v1"

// Inbox persists authenticated input before the transport acknowledges it. It
// also stores the completed reply, so delivery retries never run the model or
// knowledge mutations again. Payloads are tenant-private and never in logs.
type Inbox struct {
	ChannelID string `gorm:"primaryKey"`
	MessageID string `gorm:"primaryKey"`
	TenantID  uint64
	Input     string
	Reply     string
	Authority string
	State     string
	Attempts  int
	ErrorCode string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Inbox) TableName() string { return "octo_message_inbox" }

func (a *Adapter) accept(ctx context.Context, msg *im.IncomingMessage, handler func(context.Context, *im.IncomingMessage) error) error {
	if a.db == nil {
		return handler(ctx, msg)
	}
	// Admission happens before retention, attachment download, or model calls.
	scope, err := a.AuthorizeExecution(ctx, nil, msg)
	if err != nil {
		if receipt := a.deletionReceipt(ctx, msg); receipt != nil {
			scope, err = receipt, nil
		}
	}
	if err != nil {
		if errors.Is(err, im.ErrScopeDenied) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	row := Inbox{ChannelID: a.channelID, MessageID: msg.MessageID, TenantID: a.tenantID, Input: string(data), State: "queued", Authority: encodeAuthority(scope)}
	result := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	return a.dispatch(ctx, msg, handler)
}

func (a *Adapter) inboxQuery(ctx context.Context, id string) *gorm.DB {
	return a.db.WithContext(ctx).Model(&Inbox{}).Where("tenant_id = ? AND channel_id = ? AND message_id = ?", a.tenantID, a.channelID, id)
}

// Mark new-version processing in the existing inbox row, atomically with its
// transition to processing. Progress-card updates preserve Extra, unlike an
// ad-hoc top-level JSON field. No file journal row is needed for text-only turns.
func (a *Adapter) markGuardedProcessing(ctx context.Context, msg *im.IncomingMessage) error {
	return a.db.Session(&gorm.Session{Logger: gormlogger.Discard}).
		WithContext(ctx).
		Transaction(func(tx *gorm.DB) error {
			var row Inbox
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("tenant_id = ? AND channel_id = ? AND message_id = ?", a.tenantID, a.channelID, msg.
					MessageID).First(&row).Error; err != nil {
				return err
			}
			var original map[string]json.RawMessage
			if json.Unmarshal([]byte(row.Input), &original) != nil || original == nil {
				return errors.New("invalid stored Octo input")
			}
			extra := map[string]string{}
			if raw := original["Extra"]; len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &extra); err != nil || extra == nil {
					return errors.New("invalid stored Octo metadata")
				}
			}
			extra[fileDeliveryGuardKey] = "1"
			encodedExtra, err := json.Marshal(extra)
			if err != nil {
				return err
			}
			original["Extra"] = encodedExtra
			encoded, err := json.Marshal(original)
			if err != nil {
				return err
			}
			result := tx.Model(&Inbox{}).
				Where("tenant_id = ? AND channel_id = ? AND message_id = ?", a.tenantID, a.channelID, msg.MessageID).
				Updates(map[string]any{"state": "processing", "input": string(encoded), "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("Octo input unavailable")
			}
			return nil
		})
}

func (a *Adapter) dispatch(ctx context.Context, msg *im.IncomingMessage, handler func(context.Context, *im.IncomingMessage) error) error {
	if a.deletionReceipt(ctx, msg) != nil {
		return a.SendReply(ctx, msg, &im.ReplyMessage{Content: deletionReceiptText, IsFinal: true})
	}
	if err := a.markGuardedProcessing(ctx, msg); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, fileDeliveryOwnerKey{}, uuid.NewString())
	if err := handler(ctx, msg); err != nil {
		state := "failed"
		if errors.Is(err, im.ErrScopeDenied) {
			state = "ignored"
		}
		_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Updates(map[string]any{"state": state, "error_code": "dispatch_failed", "updated_at": time.Now()}).Error
		return err
	}
	return nil
}

// ExecutionFinished closes an admitted execution that did not already record a
// delivery outcome. A completed agent run without a final reply is a failed
// delivery, not a successful inbox outcome: treating it as finished would hide
// a lost finalizer/stream reply and leave no safe retry payload.
//
// Only pending reply delivery is retried automatically. This method must not
// synthesize another reply because a transport outcome can be ambiguous.
func (a *Adapter) ExecutionFinished(ctx context.Context, msg *im.IncomingMessage) {
	if a.db == nil || msg == nil {
		return
	}
	{
		// A mutation can commit just before its Agent deadline expires. Preserve
		// the proven fixed receipt with bounded fresh authorization, even then.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var row Inbox
		if a.inboxQuery(finishCtx, msg.MessageID).Select("state").First(&row).Error == nil && row.State == "processing" && a.deletionReceipt(finishCtx, msg) != nil {
			_ = a.SendReply(finishCtx, msg, &im.ReplyMessage{Content: deletionReceiptText, IsFinal: true})
		}
	}
	state, code := "failed", "final_reply_missing"
	if ctx.Err() != nil {
		state, code = "failed", "execution_interrupted"
	}
	_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Where("state = ?", "processing").Updates(map[string]any{"state": state, "error_code": code, "updated_at": time.Now()}).Error
}

// finishNoReply records the explicit no-reply contract separately from a
// missing final reply. The latter is a delivery failure; NO_REPLY is a terminal
// decision and must stay terminal without triggering an outbound retry.
func (a *Adapter) finishNoReply(ctx context.Context, msg *im.IncomingMessage) {
	if a.db == nil || msg == nil {
		return
	}
	if ctx.Err() != nil {
		a.ExecutionFinished(ctx, msg)
		return
	}
	_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Where("state = ?", "processing").Updates(map[string]any{
		"state":      "finished",
		"error_code": "",
		"updated_at": time.Now(),
	}).Error
}

func (a *Adapter) recoverInbox(ctx context.Context, handler func(context.Context, *im.IncomingMessage) error) {
	var cursorTime time.Time
	cursorID := ""
	for ctx.Err() == nil {
		var rows []Inbox
		query := a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND state IN ?", a.tenantID, a.channelID, []string{"queued", "processing", "reply_pending"})
		if cursorID != "" {
			query = query.Where("created_at > ? OR (created_at = ? AND message_id > ?)", cursorTime, cursorTime, cursorID)
		}
		if query.Order("created_at, message_id").Limit(20).Find(&rows).Error != nil {
			logger.Warnf(ctx, "[IM] Octo inbox recovery unavailable")
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, row := range rows {
			if ctx.Err() != nil {
				return
			}
			cursorTime, cursorID = row.CreatedAt, row.MessageID
			var msg im.IncomingMessage
			if json.Unmarshal([]byte(row.Input), &msg) != nil || msg.MessageID != row.MessageID {
				_ = a.inboxQuery(ctx, row.MessageID).Updates(map[string]any{"state": "failed", "error_code": "invalid_stored_input", "updated_at": time.Now()}).Error
				continue
			}
			if row.State == "reply_pending" {
				if row.Attempts >= 3 {
					_ = a.inboxQuery(ctx, row.MessageID).Updates(map[string]any{"state": "failed", "error_code": "delivery_retry_exhausted", "updated_at": time.Now()}).Error
					continue
				}
				_ = a.SendReply(ctx, &msg, &im.ReplyMessage{Content: row.Reply, IsFinal: true})
			} else {
				if row.State == "processing" && a.deletionReceipt(ctx, &msg) == nil {
					journal := a.fileDeliveryStore()
					if journal == nil {
						logger.Warnf(ctx, "[IM] Octo file delivery recovery unavailable")
						continue
					}
					_, unsafe, err := journal.recovery(ctx, fileTurn{
						tenant: a.tenantID, channel: a.channelID,
						source: row.MessageID,
					})
					if err != nil {
						logger.Warnf(ctx, "[IM] Octo file delivery recovery unavailable")
						continue
					}
					guarded := msg.Extra[fileDeliveryGuardKey] == "1"
					if !guarded || unsafe {
						code := "file_delivery_recovery_blocked"
						if !guarded {
							code = "file_delivery_history_unavailable"
						}
						_ = a.inboxQuery(ctx, row.MessageID).Where("state = ?", "processing").
							Updates(map[string]any{"state": "failed", "error_code": code, "updated_at": time.
								Now()}).Error
						continue
					}
				}
				_ = a.dispatch(ctx, &msg, handler)
			}
		}
	}
}

func (a *Adapter) retryDeliveries(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.markExhaustedReplies(ctx)
			var rows []Inbox
			if a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND state = ? AND attempts < ? AND updated_at < ?", a.tenantID, a.channelID, "reply_pending", 3, time.Now().Add(-25*time.Second)).Limit(20).Find(&rows).Error != nil {
				continue
			}
			for _, row := range rows {
				var msg im.IncomingMessage
				if json.Unmarshal([]byte(row.Input), &msg) == nil {
					_ = a.SendReply(ctx, &msg, &im.ReplyMessage{Content: row.Reply, IsFinal: true})
				}
			}
		}
	}
}

// A send failure that exhausts the retry budget is terminal immediately, not
// only after a later restart. A concurrent successful delivery is never reverted.
func (a *Adapter) recordReplyFailure(ctx context.Context, id, code string) {
	if a.db == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = a.inboxQuery(writeCtx, id).Where("state = ?", "reply_pending").Updates(map[string]any{
		"state":      gorm.Expr("CASE WHEN attempts >= ? THEN ? ELSE state END", 3, "failed"),
		"error_code": gorm.Expr("CASE WHEN attempts >= ? THEN ? ELSE ? END", 3, "delivery_retry_exhausted", code),
		"updated_at": time.Now(),
	}).Error
}
func (a *Adapter) markExhaustedReplies(ctx context.Context) {
	if a.db == nil {
		return
	}
	_ = a.db.WithContext(ctx).Model(&Inbox{}).Where("tenant_id = ? AND channel_id = ? AND state = ? AND attempts >= ?", a.tenantID, a.channelID, "reply_pending", 3).Updates(map[string]any{"state": "failed", "error_code": "delivery_retry_exhausted", "updated_at": time.Now()}).Error
}
