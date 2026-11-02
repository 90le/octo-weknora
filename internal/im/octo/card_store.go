package octo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type confirmationCard struct {
	TenantID    uint64
	ChannelID   string `gorm:"primaryKey"`
	ProposalID  string `gorm:"primaryKey"`
	MessageID   string
	ClientMsgNo string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (confirmationCard) TableName() string { return "octo_card_bindings" }

type storedCardEvent struct {
	TenantID    uint64
	ChannelID   string `gorm:"primaryKey"`
	EventKey    string `gorm:"primaryKey"`
	EventID     uint64
	MessageID   string
	TargetID    string
	TargetType  int
	OperatorUID string
	ActionID    string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (storedCardEvent) TableName() string { return "octo_card_events" }

type cardEventCursor struct {
	TenantID  uint64
	ChannelID string `gorm:"primaryKey"`
	EventID   uint64
	UpdatedAt time.Time
}

func (cardEventCursor) TableName() string { return "octo_card_event_cursors" }

// Event IDs can collide on the upstream queue. Deduplicate by the complete
// server-authoritative action identity rather than ID alone.
func cardEventKey(action cardAction) string {
	raw, _ := json.Marshal(
		[]any{
			action.EventID,
			action.MessageID,
			action.ChannelID,
			action.ChannelType,
			action.OperatorUID,
			action.ActionID,
		},
	)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Persist before advancing a scan cursor. Never acknowledge queue entries:
// upstream ACK removes all events with the same score, including collisions.
func (a *Adapter) recordCardEvent(ctx context.Context, action cardAction) error {
	if a.db == nil || action.EventID == 0 || len(action.Inputs) != 0 {
		return errors.New("card event store unavailable")
	}
	row := storedCardEvent{
		TenantID: a.tenantID, ChannelID: a.channelID, EventKey: cardEventKey(action), EventID: action.EventID,
		MessageID: action.MessageID, TargetID: action.ChannelID, TargetType: int(action.ChannelType),
		OperatorUID: action.OperatorUID, ActionID: action.ActionID, State: "waiting",
	}
	return a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func (a *Adapter) resetCardCursor(ctx context.Context) error {
	return a.db.WithContext(ctx).
		Model(&cardEventCursor{}).
		Where("tenant_id = ? AND channel_id = ?", a.tenantID, a.channelID).
		Updates(map[string]any{"event_id": 0, "updated_at": time.Now()}).
		Error
}

// advanceCardCursor never moves backwards, including across concurrent retries.
func (a *Adapter) advanceCardCursor(ctx context.Context, id uint64) error {
	if a.db == nil || id == 0 {
		return errors.New("invalid card cursor")
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := cardEventCursor{TenantID: a.tenantID, ChannelID: a.channelID, EventID: id}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Model(&cardEventCursor{}).
			Where("tenant_id = ? AND channel_id = ? AND event_id < ?", a.tenantID, a.channelID, id).
			Updates(map[string]any{"event_id": id, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&cardEventCursor{}).Where("tenant_id = ? AND channel_id = ?",
			a.tenantID, a.channelID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return errors.New("card cursor scope mismatch")
		}
		return nil
	})
}
