package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

type interactiveCardProfile struct {
	Enabled  bool     `json:"enabled"`
	Profiles []string `json:"profiles"`
	Elements []string `json:"elements"`
	Limits   struct {
		MaxPayloadBytes int `json:"max_payload_bytes"`
	} `json:"limits"`
	Config struct {
		CardEnabled        bool `json:"card_enabled"`
		InteractionEnabled bool `json:"interaction_enabled"`
	} `json:"config"`
}

func (p interactiveCardProfile) supportsConfirmation() bool {
	return p.Enabled && p.Config.CardEnabled && p.Config.InteractionEnabled && p.Limits.MaxPayloadBytes > 0 &&
		hasStates(p.Profiles, "octo/v2") && hasStates(p.Elements, "TextBlock")
}

// confirmationPreview must include the complete server-generated preview.
// Large or visually ambiguous text falls back to the existing typed commands.
func confirmationPreview(p *octobusiness.Proposal) (string, bool) {
	if p == nil || p.ID == "" || len(p.Result) == 0 || len(p.Result) > 6000 {
		return "", false
	}
	var preview map[string]any
	if json.Unmarshal(p.Result, &preview) != nil || preview["action"] != p.Action {
		return "", false
	}
	raw, err := json.MarshalIndent(preview, "", "  ")
	if err != nil || len(raw) > 6000 {
		return "", false
	}
	for _, r := range string(raw) {
		if unicode.Is(unicode.Cf, r) {
			return "", false
		}
	}
	// TextBlock may render Markdown. Escape link and HTML delimiters so
	// preview data cannot create a platform navigation action.
	text := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_",
		"`", "\\`", "#", "\\#", "~", "\\~", "|", "\\|", "<", "&lt;", ">", "&gt;").Replace(string(raw))
	if len(text) > 8000 {
		return "", false
	}
	return text, true
}

func confirmationCardPayload(preview string, p *octobusiness.Proposal) map[string]any {
	labels := map[string]string{"create_kb": "创建知识库", "rename": "重命名知识库", "save_draft": "保存草稿", "publish": "发布文档", "delete_document": "删除文档", "bind": "绑定知识库", "unbind": "解绑知识库", "delete_kb": "删除知识库"}
	label := labels[p.Action]
	if label == "" {
		label = "知识变更"
	}
	return map[string]any{
		"type": 17, "profile": "octo/v2", "card_version": "1.5",
		"card": map[string]any{
			"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
			"type":    "AdaptiveCard", "version": "1.5",
			"body": []any{
				map[string]any{"type": "TextBlock", "text": "请核对：" + label, "weight": "Bolder", "wrap": true},
				map[string]any{"type": "TextBlock", "text": "编号：" + p.ID + "；有效至：" + p.ExpiresAt.UTC().Format("2006-01-02 15:04:05 UTC"), "wrap": true},
				map[string]any{"type": "TextBlock", "text": preview, "wrap": true},
				map[string]any{"type": "TextBlock", "text": "仅发起人可确认或取消，操作会再次检查权限。", "wrap": true},
			},
			"actions": []any{
				map[string]any{"type": "Action.Submit", "id": "knowledge_confirm", "title": "确认"},
				map[string]any{"type": "Action.Submit", "id": "knowledge_cancel", "title": "取消"},
			},
		},
	}
}

// sendConfirmationCards is auxiliary to the durable final answer. It never
// causes a failed card send to suppress or repeat that answer.
func (a *Adapter) sendConfirmationCards(ctx context.Context, in *im.IncomingMessage) {
	if a == nil || a.db == nil || a.business == nil || in == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	scope, err := a.AuthorizeExecution(ctx, nil, in)
	if err != nil || !(&progressCard{adapter: a, message: in}).authorized(ctx) {
		return
	}
	var proposals []octobusiness.Proposal
	if a.db.WithContext(ctx).Where(
		"tenant_id = ? AND account_id = ? AND channel_id = ? AND scope_id = ? AND user_id = ? "+
			"AND source_message_id = ? AND status = ? AND expires_at > ?",
		a.tenantID, scope.AccountID, a.channelID, scope.ScopeID, in.UserID, in.MessageID, "pending", time.Now(),
	).Limit(5).Find(&proposals).Error != nil || len(proposals) == 0 {
		return
	}
	var profile interactiveCardProfile
	if a.api.request(ctx, http.MethodGet, "/v1/bot/card/profile", nil, &profile) != nil ||
		!profile.supportsConfirmation() {
		return
	}
	target, kind, ok := a.typingTarget(in)
	if !ok {
		return
	}
	for i := range proposals {
		p := &proposals[i]
		preview, ok := confirmationPreview(p)
		if !ok || !(&progressCard{adapter: a, message: in}).authorized(ctx) {
			continue
		}
		payload := confirmationCardPayload(preview, p)
		if id := in.Extra["octo_message_id"]; wire.DecimalID(id) {
			payload["reply"] = map[string]any{"message_id": id}
		}
		encodedPayload, err := json.Marshal(payload)
		if err != nil || len(encodedPayload) > profile.Limits.MaxPayloadBytes {
			continue
		}
		clientID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.ID+":confirmation-card")).String()
		binding := confirmationCard{
			TenantID: a.tenantID, ChannelID: a.channelID,
			ProposalID: p.ID, ClientMsgNo: clientID, State: "sending",
		}
		result := a.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&binding)
		if result.Error != nil || result.RowsAffected != 1 {
			continue
		}
		var sent struct {
			ID json.RawMessage `json:"message_id"`
		}
		err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
			"channel_id": target, "channel_type": kind, "client_msg_no": clientID, "payload": payload,
		}, &sent)
		messageID := wire.ReadID(sent.ID)
		if err != nil || !wire.DecimalID(messageID) {
			// An ambiguous send is never replayed automatically. The normal
			// typed confirmation remains available in the final answer.
			_ = a.db.WithContext(ctx).Model(&confirmationCard{}).
				Where("channel_id = ? AND proposal_id = ? AND state = ?", a.channelID, p.ID, "sending").
				Updates(map[string]any{"state": "uncertain", "updated_at": time.Now()}).Error
			continue
		}
		_ = a.db.WithContext(ctx).Model(&confirmationCard{}).
			Where("tenant_id = ? AND channel_id = ? AND proposal_id = ? AND state = ?",
				a.tenantID, a.channelID, p.ID, "sending").
			Updates(map[string]any{"message_id": messageID, "state": "sent", "updated_at": time.Now()}).Error
	}
}
