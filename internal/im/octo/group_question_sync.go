package octo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/octointegration"
)

const groupQuestionSyncInterval = 12 * time.Second

type syncedGroupMessage struct {
	ID        json.RawMessage `json:"message_id"`
	Sequence  uint32          `json:"message_seq"`
	Sender    string          `json:"from_uid"`
	Channel   string          `json:"channel_id"`
	Kind      byte            `json:"channel_type"`
	Timestamp uint32          `json:"timestamp"`
	Setting   byte            `json:"setting"`
	Deleted   json.RawMessage `json:"is_deleted"`
	Payload   json.RawMessage `json:"payload"`
}

// This is a second delivery path for fresh messages missed by the realtime
// receiver. Each process starts at the recent head and skips pre-start history.
// The normal inbox owns deduplication across realtime delivery and recovery.
func (a *Adapter) runGroupQuestionSync(
	ctx context.Context, account string, route func(context.Context, *im.IncomingMessage) error,
) {
	started := time.Now()
	cursors := make(map[string]uint32)
	ticker := time.NewTicker(groupQuestionSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.RuntimeStatus().State != "online" {
				continue
			}
			var scopes []octointegration.Scope
			err := a.db.WithContext(ctx).Where(
				"tenant_id = ? AND account_id = ? AND member_questions_enabled AND sync_status = ? "+
					"AND name_source = ? AND verified_at IS NOT NULL", a.tenantID, account, "verified", "octo",
			).Order("id").Find(&scopes).Error
			if err != nil {
				logger.Debugf(ctx, "[IM] Octo group recovery scope list unavailable")
				continue
			}
			seen := make(map[string]uint32, len(scopes))
			for _, scope := range scopes {
				bindings, err := octointegration.NewStore(a.db).Effective(ctx, a.tenantID, scope.ID)
				if err != nil || len(bindings) == 0 {
					continue
				}
				cutoff := started
				if scope.UpdatedAt.After(cutoff) {
					cutoff = scope.UpdatedAt
				}
				seen[scope.ID], err = a.syncGroupQuestions(ctx, scope, cursors[scope.ID], cutoff, route)
				if err != nil {
					logger.Debugf(ctx, "[IM] Octo group recovery poll incomplete for scope %s", scope.ID)
				}
			}
			cursors = seen
		}
	}
}

func (a *Adapter) syncGroupQuestions(
	ctx context.Context, scope octointegration.Scope, cursor uint32, cutoff time.Time,
	route func(context.Context, *im.IncomingMessage) error,
) (uint32, error) {
	target, kind := scope.GroupID, byte(2)
	if scope.SubareaID != "" {
		target, kind = target+"____"+scope.SubareaID, 5
	}
	if _, err := wire.ParseScope(target, kind); err != nil || cursor == math.MaxUint32 {
		return cursor, err
	}
	var page struct {
		Messages []syncedGroupMessage `json:"messages"`
	}
	start := uint32(0) // Octo returns the latest page for a zero start.
	if cursor > 0 {
		start = cursor + 1
	}
	request := map[string]any{
		"channel_id": target, "channel_type": kind, "start_message_seq": start,
		"end_message_seq": 0, "limit": 25, "pull_mode": 1,
	}
	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := a.api.request(pollCtx, http.MethodPost, "/v1/bot/messages/sync", request, &page)
	cancel()
	if err != nil {
		return cursor, err
	}
	slices.SortFunc(page.Messages, func(a, b syncedGroupMessage) int {
		switch {
		case a.Sequence < b.Sequence:
			return -1
		case a.Sequence > b.Sequence:
			return 1
		default:
			return 0
		}
	})
	for _, entry := range page.Messages {
		if entry.Sequence <= cursor || entry.Channel != target || entry.Kind != kind {
			continue
		}
		if err := a.acceptSyncedGroupQuestion(ctx, entry, cutoff, route); err != nil {
			return cursor, err
		}
		cursor = entry.Sequence
	}
	return cursor, nil
}

func (a *Adapter) acceptSyncedGroupQuestion(
	ctx context.Context, entry syncedGroupMessage, cutoff time.Time,
	route func(context.Context, *im.IncomingMessage) error,
) error {
	id := wire.ReadID(entry.ID)
	if id == "" || flag(entry.Deleted) || int64(entry.Timestamp) <= cutoff.Unix() ||
		entry.Sender == a.uid || entry.Setting&2 != 0 || len(entry.Payload) == 0 {
		return nil
	}
	payload := entry.Payload
	if payload[0] == '"' {
		var encoded string
		if json.Unmarshal(payload, &encoded) != nil {
			return nil
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil
		}
		payload = decoded
	}
	msg, err := a.Normalize(&wire.Message{
		ID: id, Sender: entry.Sender, Channel: entry.Channel, ChannelType: entry.Kind,
		Payload: payload, Sequence: entry.Sequence, Timestamp: entry.Timestamp,
	})
	if err != nil || msg == nil || a.ignoresGroupMessage(msg) {
		return nil
	}
	var retained int64
	if err := a.inboxQuery(ctx, msg.MessageID).Count(&retained).Error; err != nil || retained != 0 {
		return err
	}
	msg.Extra["octo_group_sync_recovered"] = "true"
	return a.accept(ctx, msg, route)
}
