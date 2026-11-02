package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Progress cards are independent of durable answer delivery. Only fixed labels
// leave the process; raw thoughts, tool arguments and retrieval results do not.
// Groups require an explicit native mention or a reply to this Bot.
type progressCard struct {
	adapter *Adapter
	message *im.IncomingMessage
	updates chan string
	finish  chan im.ProcessingResult
	done    chan struct{}
	once    sync.Once
}

type cardTemplateRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type progressProfile struct {
	Enabled bool `json:"enabled"`
	Config  struct {
		CardEnabled      bool             `json:"card_enabled"`
		ReasoningEnabled bool             `json:"reasoning_enabled"`
		ReasoningRef     *cardTemplateRef `json:"reasoning_template_ref"`
	} `json:"config"`
	Templating struct {
		Supported bool   `json:"supported"`
		Wire      string `json:"wire"`
		Templates []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
			Views   []struct {
				Name        string   `json:"name"`
				States      []string `json:"states"`
				WireProfile string   `json:"wire_profile"`
				Actions     []string `json:"submit_actions"`
			} `json:"views"`
		} `json:"templates"`
	} `json:"templating"`
}

func (p progressProfile) supported() bool {
	ref := p.Config.ReasoningRef
	if !p.Enabled || !p.Config.CardEnabled || !p.Config.ReasoningEnabled || ref == nil ||
		ref.ID != "ai.reasoning-process" || ref.Version != "0.4.1" ||
		!p.Templating.Supported || p.Templating.Wire != "template-ref/v1" {
		return false
	}
	matches := 0
	for _, template := range p.Templating.Templates {
		if template.ID != ref.ID || template.Version != ref.Version {
			continue
		}
		matches++
		active, result, failure := false, false, false
		for _, view := range template.Views {
			// This producer does not consume submit callbacks.
			if len(view.Actions) != 0 {
				return false
			}
			switch view.Name {
			case "active":
				active = view.WireProfile == "octo/v2" && hasStates(view.States, "reasoning", "answering")
			case "result":
				result = view.WireProfile == "octo/v1" && hasStates(view.States, "completed", "stopped")
			case "error":
				failure = view.WireProfile == "octo/v2" && hasStates(view.States, "error")
			}
		}
		if !active || !result || !failure {
			return false
		}
	}
	return matches == 1
}

func hasStates(states []string, required ...string) bool {
	for _, want := range required {
		found := false
		for _, state := range states {
			found = found || state == want
		}
		if !found {
			return false
		}
	}
	return true
}

var _ im.ProcessingProgressNotifier = (*Adapter)(nil)

func (a *Adapter) StartProcessingProgress(ctx context.Context, msg *im.IncomingMessage) im.ProcessingProgress {
	if a == nil || a.db == nil || msg == nil || ctx.Err() != nil {
		return nil
	}
	if msg.ChatType != im.ChatTypeDirect && (msg.ChatType != im.ChatTypeGroup ||
		(msg.Extra["octo_addressed"] != "true" && (msg.Quote == nil || !msg.Quote.IsBotMessage))) {
		return nil
	}
	if _, _, ok := a.typingTarget(msg); !ok {
		return nil
	}
	p := &progressCard{adapter: a, message: msg, updates: make(chan string, 1), finish: make(chan im.ProcessingResult, 1), done: make(chan struct{})}
	go p.run(ctx)
	return p
}

func (p *progressCard) Report(stage string) {
	switch stage {
	case "knowledge", "source", "answer":
		select {
		case p.updates <- stage:
		default:
		}
	}
}

func (p *progressCard) Finish(result im.ProcessingResult) {
	p.once.Do(func() { p.finish <- result })
}

func (p *progressCard) authorized(ctx context.Context) bool {
	scope, err := p.adapter.AuthorizeExecution(ctx, nil, p.message)
	if err != nil {
		return false
	}
	var saved Inbox
	err = p.adapter.inboxQuery(ctx, p.message.MessageID).Select("authority", "state").First(&saved).Error
	return err == nil && saved.State != "ignored" && permitsReply(saved.Authority, scope)
}

// Record only the platform card identifier in the existing durable inbox.
// The original message and its authorization fingerprint remain unchanged.
func (p *progressCard) saveProgress(ctx context.Context, id string, closed bool) error {
	return p.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Inbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND channel_id = ? AND message_id = ?",
				p.adapter.tenantID, p.adapter.channelID, p.message.MessageID).First(&row).Error; err != nil {
			return err
		}
		var stored im.IncomingMessage
		if err := json.Unmarshal([]byte(row.Input), &stored); err != nil {
			return err
		}
		if stored.Extra == nil {
			stored.Extra = make(map[string]string)
		}
		stored.Extra["octo_progress_message_id"] = id
		if closed {
			stored.Extra["octo_progress_closed"] = "true"
		}
		raw, err := json.Marshal(&stored)
		if err != nil {
			return err
		}
		return tx.Model(&Inbox{}).Where("tenant_id = ? AND channel_id = ? AND message_id = ?",
			p.adapter.tenantID, p.adapter.channelID, p.message.MessageID).UpdateColumn("input", string(raw)).Error
	})
}

func progressData(id, state, stage string) map[string]any {
	title, detail, tone := "处理中", "正在理解问题", "Accent"
	switch stage {
	case "knowledge":
		detail = "正在检索已授权的知识库"
	case "source":
		detail = "正在查阅已授权的资料"
	case "answer":
		title, detail = "正在回答", "正在整理回答"
	}
	if state == "completed" {
		title, detail, tone = "已完成", "处理完成", "Good"
	} else if state == "stopped" {
		title, detail, tone = "已停止", "本次处理已停止", "Attention"
	} else if state == "error" {
		title, detail, tone = "已结束", "处理未能正常完成", "Attention"
	}
	active := state == "reasoning" || state == "answering"
	data := map[string]any{
		"reasoningId": id, "state": state, "title": "处理进度", "statusLabel": title,
		"statusTone": tone, "traceExpanded": active, "traceCollapsed": !active,
		"collapsedSummary": detail,
		"phases": []any{map[string]any{"thought": "处理本次问题", "actions": []any{
			map[string]any{"tool": "处理", "detail": detail, "statusGlyph": "●", "statusTone": tone},
		}}},
	}
	if active {
		data["progressText"] = detail
	}
	if state == "error" {
		data["errorTitle"], data["errorMessage"] = "处理未完成", "请查看最终回复或稍后重试。"
	}
	return data
}

func (p *progressCard) run(ctx context.Context) {
	defer close(p.done)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var profile progressProfile
	err := p.adapter.api.request(callCtx, http.MethodGet, "/v1/bot/card/profile", nil, &profile)
	if err != nil || !profile.supported() || !p.authorized(callCtx) {
		cancel()
		return
	}
	var pending int64
	if p.adapter.inboxQuery(callCtx, p.message.MessageID).Where("state = ?", "processing").Count(&pending).Error != nil || pending != 1 {
		cancel()
		return
	}
	select {
	case <-p.finish:
		cancel()
		return
	default:
	}
	target, kind, ok := p.adapter.typingTarget(p.message)
	if !ok {
		cancel()
		return
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.message.MessageID+":progress")).String()
	ref := *profile.Config.ReasoningRef
	messageID := p.message.Extra["octo_progress_message_id"]
	if p.message.Extra["octo_progress_closed"] == "true" {
		cancel()
		return
	}
	if !wire.DecimalID(messageID) {
		var sent struct {
			ID json.RawMessage `json:"message_id"`
		}
		err = p.adapter.api.post(callCtx, "/v1/bot/sendMessage", map[string]any{
			"channel_id": target, "channel_type": kind, "client_msg_no": id,
			"payload": map[string]any{"type": 17, "template_ref": ref, "state": "reasoning", "data": progressData(id, "reasoning", "")},
		}, &sent)
		messageID = wire.ReadID(sent.ID)
		if err != nil || !wire.DecimalID(messageID) {
			cancel()
			return
		}
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := p.saveProgress(saveCtx, messageID, false)
		saveCancel()
		if saveErr != nil {
			cancel()
			return
		}
	}
	cancel()
	lastSeq := time.Now().UnixMicro()
	edit := func(state, stage string, transient bool) {
		outCtx, outCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer outCancel()
		if !p.authorized(outCtx) {
			return
		}
		lastSeq = max(lastSeq+1, time.Now().UnixMicro())
		body := map[string]any{
			"message_id": messageID, "channel_id": target, "channel_type": kind,
			"template_ref": ref, "state": state, "data": progressData(id, state, stage),
			"card_seq": lastSeq,
		}
		if transient {
			body["transient"] = true
		}
		if p.adapter.api.post(outCtx, "/v1/bot/message/edit", body, nil) == nil && !transient {
			var row Inbox
			// A failed final send can still be retried. Keep its card eligible
			// for recovery so a later delivery can replace the error state.
			if p.adapter.inboxQuery(outCtx, p.message.MessageID).Select("state").First(&row).Error == nil &&
				row.State != "reply_pending" {
				_ = p.saveProgress(outCtx, messageID, true)
			}
		}
	}
	stage := ""
	dirty := false
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case next := <-p.updates:
			if next != stage {
				stage = next
				dirty = true
			}
		case <-ticker.C:
			if dirty {
				state := "reasoning"
				if stage == "answer" {
					state = "answering"
				}
				edit(state, stage, true)
				dirty = false
			}
		case result := <-p.finish:
			state := string(result)
			if state != "completed" && state != "stopped" {
				state = "error"
			}
			edit(state, stage, false)
			return
		case <-ctx.Done():
			edit("error", stage, false)
			return
		}
	}
}

// A restart can occur after the final answer is recorded but before the
// auxiliary card is finalized. Recover only cards created by this adapter;
// never re-run QA or alter the durable answer.
func (a *Adapter) recoverProgressCards(ctx context.Context) {
	if a.db == nil {
		return
	}
	var rows []Inbox
	err := a.db.WithContext(ctx).
		Where("tenant_id = ? AND channel_id = ? AND state IN ? AND updated_at > ? AND input LIKE ? AND input NOT LIKE ?",
			a.tenantID, a.channelID, []string{"delivered", "failed", "finished"},
			time.Now().Add(-24*time.Hour), "%octo_progress_message_id%", `%"octo_progress_closed":"true"%`).
		Order("updated_at DESC").Limit(100).Find(&rows).Error
	if err != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		var msg im.IncomingMessage
		if json.Unmarshal([]byte(row.Input), &msg) != nil || msg.MessageID != row.MessageID ||
			msg.Extra["octo_progress_closed"] == "true" ||
			!wire.DecimalID(msg.Extra["octo_progress_message_id"]) {
			continue
		}
		p := &progressCard{adapter: a, message: &msg}
		p.finishRecovered(ctx, row.State)
	}
}

func (p *progressCard) finishRecovered(ctx context.Context, deliveryState string) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var profile progressProfile
	if p.adapter.api.request(callCtx, http.MethodGet, "/v1/bot/card/profile", nil, &profile) != nil ||
		!profile.supported() || !p.authorized(callCtx) {
		return
	}
	target, kind, ok := p.adapter.typingTarget(p.message)
	if !ok {
		return
	}
	state := "error"
	if deliveryState == "delivered" {
		state = "completed"
	} else if deliveryState == "finished" {
		state = "stopped"
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.message.MessageID+":progress")).String()
	cardID := p.message.Extra["octo_progress_message_id"]
	err := p.adapter.api.post(callCtx, "/v1/bot/message/edit", map[string]any{
		"message_id": cardID, "channel_id": target, "channel_type": kind,
		"template_ref": *profile.Config.ReasoningRef, "state": state,
		"data": progressData(id, state, ""), "card_seq": time.Now().UnixMicro(),
	}, nil)
	if err == nil {
		_ = p.saveProgress(callCtx, cardID, true)
	}
}
