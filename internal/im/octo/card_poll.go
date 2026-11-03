package octo

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"gorm.io/gorm"
)

// Report operational conditions without logging event contents or user input.
func (a *Adapter) warnCardPolling(ctx context.Context, code string) {
	a.cardDiagMu.Lock()
	defer a.cardDiagMu.Unlock()
	if a.cardDiagAt == nil {
		a.cardDiagAt = make(map[string]time.Time)
	}
	if time.Since(a.cardDiagAt[code]) < time.Minute {
		return
	}
	a.cardDiagAt[code] = time.Now()
	logger.Warnf(ctx, "[IM] Octo confirmation event polling issue: code=%s channel=%s", code, a.channelID)
}

func readBotEventID(raw json.RawMessage) (uint64, error) {
	var header struct {
		ID json.Number `json:"event_id"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return 0, errors.New("invalid bot event")
	}
	id, err := strconv.ParseUint(string(header.ID), 10, 63)
	if err != nil || id == 0 {
		return 0, errors.New("invalid bot event ID")
	}
	return id, nil
}

// runConfirmationEvents never ACKs events: upstream ACK deletes all events
// sharing the score, and real queues may contain score collisions. Re-scan
// from zero after every complete pass to catch late lower-score publications.
func (a *Adapter) runConfirmationEvents(
	ctx context.Context,
	handler func(context.Context, *im.IncomingMessage) error,
) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		a.pollConfirmationEvents(ctx, handler)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Adapter) pollConfirmationEvents(
	ctx context.Context, handler func(context.Context, *im.IncomingMessage) error,
) {
	if a.db == nil || a.business == nil || ctx.Err() != nil {
		return
	}
	// Recover durable work independently of polling or pending proposal state.
	a.processWaitingCardEvents(ctx, handler)
	var count int64
	if a.db.WithContext(ctx).Table("octo_card_bindings AS c").
		Joins("JOIN octo_knowledge_proposals AS p ON p.id = c.proposal_id "+
			"AND p.tenant_id = c.tenant_id AND p.channel_id = c.channel_id").
		Where("c.tenant_id = ? AND c.channel_id = ? AND c.state = ? AND p.status = ? AND p.expires_at > ?",
			a.tenantID, a.channelID, "sent", "pending", time.Now()).
		Count(&count).Error != nil || count == 0 {
		return
	}
	var cursor cardEventCursor
	err := a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ?", a.tenantID, a.channelID).
		First(&cursor).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cursor.EventID = 0
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var response struct {
		Results []json.RawMessage `json:"results"`
	}
	const pageSize = 100
	if a.api.post(
		callCtx,
		"/v1/bot/events",
		map[string]any{"event_id": cursor.EventID, "limit": pageSize},
		&response,
	) != nil {
		a.warnCardPolling(ctx, "api_unavailable")
		return
	}
	type entry struct {
		id  uint64
		raw json.RawMessage
	}
	events := make([]entry, 0, len(response.Results))
	for _, raw := range response.Results {
		id, err := readBotEventID(raw)
		if err != nil || id <= cursor.EventID {
			a.warnCardPolling(ctx, "invalid_event_page")
			return
		}
		events = append(events, entry{id: id, raw: raw})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].id < events[j].id })
	for _, event := range events {
		action, err := parseCardAction(event.raw)
		if err == nil && len(action.Inputs) == 0 &&
			(action.ActionID == "knowledge_confirm" || action.ActionID == "knowledge_cancel") {
			if a.recordCardEvent(callCtx, action) != nil {
				return
			}
		}
	}
	if len(events) < pageSize {
		if a.resetCardCursor(callCtx) != nil {
			return
		}
	} else {
		// Never cross the final score of a full page: another colliding item
		// may be just beyond the page boundary. If all 100 scores coincide,
		// stay put rather than silently discard the remainder.
		last := events[len(events)-1].id
		i := len(events) - 1
		for i >= 0 && events[i].id == last {
			i--
		}
		if i >= 0 && a.advanceCardCursor(callCtx, events[i].id) != nil {
			return
		}
		if i < 0 {
			a.warnCardPolling(ctx, "same_score_page_stalled")
		}
	}
	a.processWaitingCardEvents(ctx, handler)
}

func (a *Adapter) processWaitingCardEvents(
	ctx context.Context, handler func(context.Context, *im.IncomingMessage) error,
) {
	var events []storedCardEvent
	if a.db.WithContext(ctx).Where("tenant_id = ? AND channel_id = ? AND state = ?",
		a.tenantID, a.channelID, "waiting").
		Order("updated_at, event_id").Limit(50).Find(&events).Error != nil {
		return
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return
		}
		recognized, err := a.acceptConfirmationEvent(ctx, event, handler)
		if err != nil {
			// Retry this event later without letting it starve newer clicks.
			_ = a.setCardEventState(ctx, event.EventKey, "waiting")
			a.warnCardPolling(ctx, "event_retry")
			continue
		}
		if !recognized && event.CreatedAt.Before(time.Now().Add(-15*time.Minute)) {
			_ = a.setCardEventState(ctx, event.EventKey, "unmatched")
		} else if !recognized {
			// Rotate unknown events so they cannot starve newer card clicks.
			_ = a.setCardEventState(ctx, event.EventKey, "waiting")
		}
	}
}
