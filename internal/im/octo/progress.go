package octo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	updates chan struct{}
	finish  chan im.ProcessingResult
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	history []string
	started time.Time
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

// StartProcessingProgress creates a capability-gated card for an admitted message.
func (a *Adapter) StartProcessingProgress(
	ctx context.Context,
	msg *im.IncomingMessage,
) im.ProcessingProgress {
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
	p := &progressCard{
		adapter: a, message: msg, updates: make(chan struct{}, 1), started: time.Now(),
		finish: make(chan im.ProcessingResult, 1), done: make(chan struct{}),
	}
	go p.run(ctx)
	return p
}

func (p *progressCard) Report(stage string) {
	if _, _, ok := progressStageText(stage); !ok {
		return
	}
	p.mu.Lock()
	p.history = compactProgressStage(p.history, stage)
	p.mu.Unlock()
	select {
	case p.updates <- struct{}{}:
	default:
	}
}

// Keep one entry per invocation rather than displaying its start and finish as
// separate steps. A retry of the same failed step is retained as one entry.
func compactProgressStage(history []string, stage string) []string {
	if _, _, ok := progressStageText(stage); !ok {
		return history
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		if last == stage {
			return history
		}
		base, outcome, _ := strings.Cut(stage, ":")
		previousBase, previousOutcome, _ := strings.Cut(last, ":")
		if base == previousBase && base != "earlier" {
			switch {
			case previousOutcome == "" && (outcome == "done" || outcome == "failed"):
				history[len(history)-1] = stage
				return history
			case previousOutcome == "failed" && outcome == "":
				history[len(history)-1] = base + ":retry"
				return history
			case previousOutcome == "retry" && (outcome == "done" || outcome == "failed"):
				if outcome == "done" {
					stage = base + ":retried"
				}
				history[len(history)-1] = stage
				return history
			}
		}
	}
	history = append(history, stage)
	if len(history) > 6 {
		history = append([]string{"earlier"}, history[len(history)-5:]...)
	}
	return history
}

func (p *progressCard) progressHistory() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.history...)
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
func (p *progressCard) saveProgress(ctx context.Context, id string, closed bool, history ...[]string) error {
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
		if len(history) > 0 && len(history[0]) > 0 {
			encoded, _ := json.Marshal(history[0])
			stored.Extra["octo_progress_history"] = string(encoded)
		}
		if closed {
			stored.Extra["octo_progress_closed"] = "true"
		}
		raw, err := json.Marshal(&stored)
		if err != nil {
			return err
		}
		return tx.Model(&Inbox{}).Where("tenant_id = ? AND channel_id = ? AND message_id = ?",
			p.adapter.tenantID, p.adapter.channelID, p.message.MessageID).
			UpdateColumn("input", string(raw)).Error
	})
}

// progressStageText returns only fixed, public descriptions. A result describes
// one completed tool invocation, never the quality or quantity of its output.
func progressStageText(stage string) (string, string, bool) {
	if stage == "earlier" {
		return "较早的处理步骤已省略", "Accent", true
	}
	labels := map[string]string{
		"knowledge": "检索已授权的知识库", "documents": "查阅已授权的文档",
		"source": "查阅已授权的资料", "configuration": "核对当前区域配置",
		"preview": "准备知识变更预览", "confirmation": "处理知识变更确认",
		"contacts": "查找资料负责人", "issues": "处理知识反馈", "report": "生成知识报告",
		"answer": "整理答复",
	}
	base, outcome, _ := strings.Cut(stage, ":")
	label, ok := labels[base]
	if !ok {
		return "", "", false
	}
	switch outcome {
	case "":
		return "正在" + label, "Accent", true
	case "done":
		return label + "步骤已返回", "Good", true
	case "failed":
		return label + "步骤未成功", "Attention", true
	case "retry":
		return "正在重试" + label, "Accent", true
	case "retried":
		return label + "步骤已返回（重试后）", "Good", true
	default:
		return "", "", false
	}
}

func progressData(id, state string, history []string, waiting bool, elapsed time.Duration) map[string]any {
	title, detail, tone := "处理中", "已收到请求，正在处理", "Accent"
	var compact []string
	for _, stage := range history {
		compact = compactProgressStage(compact, stage)
	}
	phases := make([]any, 0, 6)
	for _, stage := range compact {
		text, stepTone, ok := progressStageText(stage)
		if !ok {
			continue
		}
		detail = text
		hint := "处理仍在进行"
		switch {
		case stage == "earlier":
			hint = "仅显示最近的处理步骤"
		case strings.HasSuffix(stage, ":retried"):
			hint = "已自动重试，结果以最终答复为准"
		case strings.HasSuffix(stage, ":done"):
			hint = "调用已返回，结果以最终答复为准"
		case strings.HasSuffix(stage, ":failed"):
			hint = "本步骤未成功，详情以最终答复为准"
		case strings.HasSuffix(stage, ":retry"):
			hint = "正在自动重试"
		}
		if stage == "answer" && state == "completed" {
			text, stepTone, hint = "答复已整理", "Good", "最终答复已发送"
		}
		phases = append(phases, map[string]any{"thought": text, "actions": []any{
			map[string]any{"tool": "说明", "detail": hint, "statusGlyph": "●", "statusTone": stepTone},
		}})
	}
	if len(phases) == 0 {
		phases = []any{map[string]any{"thought": "已收到请求", "actions": []any{
			map[string]any{"tool": "说明", "detail": "正在准备处理", "statusGlyph": "●", "statusTone": "Accent"},
		}}}
	}
	switch state {
	case "answering":
		title = "正在回答"
	case "completed":
		title, detail, tone = "答复已发送", "本次答复已发送", "Good"
		if waiting {
			title, detail = "预览待确认", "预览已生成，变更尚未执行"
		}
		if elapsed >= time.Second {
			detail += fmt.Sprintf(" · 用时 %d 秒", int(elapsed.Seconds()))
		}
	case "stopped":
		title, detail, tone = "已停止", "本次处理已停止", "Attention"
	case "error":
		title, detail, tone = "未完成", "处理未能正常完成", "Attention"
	}
	active := state == "reasoning" || state == "answering"
	data := map[string]any{
		"reasoningId": id, "state": state, "title": "处理进度", "statusLabel": title,
		"statusTone": tone, "traceExpanded": active, "traceCollapsed": !active,
		"collapsedSummary": detail, "phases": phases,
	}
	if active {
		data["progressText"] = detail
	}
	if state == "error" {
		data["errorTitle"], data["errorMessage"] = "处理未完成", "请查看最终回复或稍后重试。"
	}
	return data
}

func (p *progressCard) hasPendingPreview(ctx context.Context) bool {
	if p.adapter.business == nil {
		return false
	}
	var count int64
	err := p.adapter.db.WithContext(ctx).Table("octo_knowledge_proposals").
		Where("tenant_id = ? AND channel_id = ? AND source_message_id = ? AND user_id = ? "+
			"AND status = ? AND expires_at > ?",
			p.adapter.tenantID, p.adapter.channelID, p.message.MessageID, p.message.UserID, "pending", time.Now()).
		Count(&count).Error
	return err == nil && count > 0
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
	err = p.adapter.inboxQuery(callCtx, p.message.MessageID).
		Where("state = ?", "processing").
		Count(&pending).
		Error
	if err != nil || pending != 1 {
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
			"payload": map[string]any{
				"type": 17, "template_ref": ref, "state": "reasoning",
				"data": progressData(id, "reasoning", p.progressHistory(), false, 0),
			},
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
	edit := func(state string, transient bool) {
		outCtx, outCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer outCancel()
		if !p.authorized(outCtx) {
			return
		}
		history := p.progressHistory()
		_ = p.saveProgress(outCtx, messageID, false, history)
		waiting := state == "completed" && p.hasPendingPreview(outCtx)
		elapsed := time.Duration(0)
		if !p.started.IsZero() {
			elapsed = time.Since(p.started)
		}
		lastSeq = max(lastSeq+1, time.Now().UnixMicro())
		body := map[string]any{
			"message_id": messageID, "channel_id": target, "channel_type": kind,
			"template_ref": ref, "state": state, "data": progressData(id, state, history, waiting, elapsed),
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
				_ = p.saveProgress(outCtx, messageID, true, history)
			}
		}
	}
	dirty := false
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.updates:
			dirty = true
		case <-ticker.C:
			if dirty {
				state := "reasoning"
				history := p.progressHistory()
				if len(history) > 0 && history[len(history)-1] == "answer" {
					state = "answering"
				}
				edit(state, true)
				dirty = false
			}
		case result := <-p.finish:
			edit(progressResultState(result), false)
			return
		case <-ctx.Done():
			// Finish is queued before the request's deferred cancellation. Both
			// channels can be ready at once; prefer the actual delivery result.
			edit(p.resultAfterCancellation(), false)
			return
		}
	}
}

func progressResultState(result im.ProcessingResult) string {
	if result == im.ProcessingCompleted || result == im.ProcessingStopped {
		return string(result)
	}
	return "error"
}

func (p *progressCard) resultAfterCancellation() string {
	select {
	case result := <-p.finish:
		return progressResultState(result)
	default:
		return "error"
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
		Where("tenant_id = ? AND channel_id = ? AND state IN ? AND updated_at > ? "+
			"AND input LIKE ? AND input NOT LIKE ?",
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
	switch deliveryState {
	case "delivered":
		state = "completed"
	case "finished":
		state = "stopped"
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.message.MessageID+":progress")).String()
	cardID := p.message.Extra["octo_progress_message_id"]
	var history []string
	_ = json.Unmarshal([]byte(p.message.Extra["octo_progress_history"]), &history)
	if len(history) > 6 {
		history = history[len(history)-6:]
	}
	err := p.adapter.api.post(callCtx, "/v1/bot/message/edit", map[string]any{
		"message_id": cardID, "channel_id": target, "channel_type": kind,
		"template_ref": *profile.Config.ReasoningRef, "state": state,
		"data":     progressData(id, state, history, state == "completed" && p.hasPendingPreview(callCtx), 0),
		"card_seq": time.Now().UnixMicro(),
	}, nil)
	if err == nil {
		_ = p.saveProgress(callCtx, cardID, true, history)
	}
}
