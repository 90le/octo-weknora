package octo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
		if errors.Is(err, im.ErrScopeDenied) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	row := Inbox{ChannelID: a.channelID, MessageID: msg.MessageID, TenantID: a.tenantID, Input: string(data), State: "queued", Authority: im.ExecutionScopeFingerprint(scope)}
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

func (a *Adapter) dispatch(ctx context.Context, msg *im.IncomingMessage, handler func(context.Context, *im.IncomingMessage) error) error {
	if err := a.inboxQuery(ctx, msg.MessageID).Updates(map[string]any{"state": "processing", "updated_at": time.Now()}).Error; err != nil {
		return err
	}
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

// ExecutionFinished accounts for silence/cancellation/authorization changes as
// terminal outcomes. Only pending reply delivery is retried automatically.
func (a *Adapter) ExecutionFinished(ctx context.Context, msg *im.IncomingMessage) {
	if a.db == nil || msg == nil {
		return
	}
	state, code := "finished", ""
	if ctx.Err() != nil {
		state, code = "failed", "execution_interrupted"
	}
	_ = a.inboxQuery(context.WithoutCancel(ctx), msg.MessageID).Where("state = ?", "processing").Updates(map[string]any{"state": state, "error_code": code, "updated_at": time.Now()}).Error
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
