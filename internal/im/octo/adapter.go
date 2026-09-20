// Package octo adapts Octo messages to WeKnora's native IM contract.
// It deliberately has no default factory registration until scoped authorization
// is wired into the IM execution path. Transport availability is not KB access.
package octo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const Platform im.Platform = "octo"

type Adapter struct {
	db        *gorm.DB
	channelID string
	tenantID  uint64
	api       *apiClient
	uid       string
	policy    func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error)
}

func (a *Adapter) AuthorizeExecution(ctx context.Context, channel *im.IMChannel, msg *im.IncomingMessage) (*im.ExecutionScope, error) {
	if a.policy == nil {
		return nil, im.ErrScopeDenied
	}
	return a.policy(ctx, channel, msg)
}

var _ im.Adapter = (*Adapter)(nil)
var _ im.FileDownloader = (*Adapter)(nil)

func NewAdapter(botToken, botUID string) (*Adapter, error) {
	if botUID == "" || strings.ContainsAny(botUID, "\r\n\t ") {
		return nil, errors.New("Octo Bot UID required")
	}
	api, err := newAPI(botToken)
	if err != nil {
		return nil, err
	}
	return &Adapter{api: api, uid: botUID}, nil
}
func (a *Adapter) Platform() im.Platform { return Platform }

// DurableIntake tells the native dispatcher that the authenticated receiver has
// already persisted and claimed this input; a volatile dedup cache must not
// discard recovery after a process restart.
func (a *Adapter) DurableIntake() bool { return a.db != nil }

// Octo input is authenticated on the WebSocket. Never expose an unsigned HTTP fallback.
func (a *Adapter) VerifyCallback(*gin.Context) error {
	return errors.New("Octo uses authenticated WebSocket input")
}
func (a *Adapter) ParseCallback(*gin.Context) (*im.IncomingMessage, error) {
	return nil, errors.New("Octo HTTP callback unsupported")
}
func (a *Adapter) HandleURLVerification(*gin.Context) bool { return false }

// Normalize does not authorize a user. Its result must pass a trusted scope and
// sender policy before being submitted to im.Service.HandleMessage.
func (a *Adapter) Normalize(raw *wire.Message) (*im.IncomingMessage, error) {
	if raw == nil {
		return nil, errors.New("invalid Octo message")
	}
	// Transient/system packets and other Agents' stream fragments are not user
	// questions. Acknowledge and ignore them instead of stopping the receiver.
	if raw.Stream || !wire.DecimalID(raw.ID) || raw.Sender == "" {
		return nil, nil
	}
	if raw.Sender == a.uid {
		return nil, nil
	}
	scope, err := wire.ParseScope(raw.Channel, raw.ChannelType)
	if err != nil {
		return nil, err
	}
	var p wire.Payload
	if json.Unmarshal(raw.Payload, &p) != nil {
		return nil, errors.New("invalid Octo payload")
	}
	m := &im.IncomingMessage{Platform: Platform, UserID: raw.Sender, UserName: raw.Sender, MessageID: "octo:" + a.uid + ":" + raw.ID, Content: p.Text(), ChatType: im.ChatTypeGroup, ChatID: raw.Channel, MessageType: im.MessageTypeText,
		Extra: map[string]string{"octo_message_id": raw.ID, "octo_channel_id": raw.Channel, "octo_channel_type": strconv.Itoa(int(raw.ChannelType)), "octo_group_id": scope.Group, "octo_subarea_id": scope.Subarea, "octo_original_payload": string(raw.Payload), "octo_addressed": "false"}}
	if raw.ChannelType == 1 {
		m.ChatType = im.ChatTypeDirect
		m.ChatID = ""
	}
	if p.AddressedTo(a.uid) {
		m.Extra["octo_addressed"] = "true"
		if command, ok := leadingNativeCommandText(m.Content, p.Mention.Entities, a.uid); ok {
			m.Extra["octo_command_text"] = command
		}
	}
	switch p.Type {
	case 1, 14:
		if strings.TrimSpace(m.Content) == "" {
			return nil, nil
		}
	case 2, 3, 8:
		m.MessageType = im.MessageTypeFile
		if p.Type != 8 {
			m.MessageType = im.MessageTypeImage
		}
		m.FileKey = p.URL
		m.FileName = path.Base(strings.ReplaceAll(p.Name, "\\", "/"))
		m.FileSize = p.Size
		if m.FileKey == "" {
			return nil, errors.New("missing Octo attachment URL")
		}
		if p.Name == "" || m.FileName == "." {
			m.FileName = "attachment"
			if p.Type == 2 {
				m.FileName += ".png"
			}
			if p.Type == 3 {
				m.FileName += ".gif"
			}
		}
	default:
		return nil, nil // structured platform events are not user questions
	}
	if p.Reply != nil {
		m.Quote = &im.QuotedMessage{MessageID: wire.ReadID(p.Reply.ID), Content: p.Reply.Payload.Text(), SenderID: p.Reply.Sender, IsBotMessage: p.Reply.Sender == a.uid}
		if m.Quote.Content == "" && p.Reply.Payload != nil {
			m.Quote.NonTextType = "attachment"
		}
	}
	return m, nil
}

// Octo entity offsets are UTF-16 code units. A renamed local channel is not
// evidence of the Bot's visible mention label, so normalize only the native
// leading entity for this exact Bot UID and leave the retrieval text intact.
func leadingNativeCommandText(text string, entities []wire.Entity, uid string) (string, bool) {
	units := utf16.Encode([]rune(text))
	for _, entity := range entities {
		if entity.UID != uid || entity.Offset != 0 || entity.Length <= 0 || entity.Length > len(units) {
			continue
		}
		end := entity.Length
		if end < len(units) && units[end-1] >= 0xD800 && units[end-1] <= 0xDBFF && units[end] >= 0xDC00 && units[end] <= 0xDFFF {
			continue
		}
		label := string(utf16.Decode(units[:end]))
		if !strings.HasPrefix(label, "@") || strings.ContainsAny(label, "\r\n\t") {
			continue
		}
		rest := []rune(string(utf16.Decode(units[end:])))
		if len(rest) > 0 && !unicode.IsSpace(rest[0]) {
			continue
		}
		return strings.TrimSpace(string(rest)), true
	}
	return "", false
}

func (a *Adapter) SendReply(ctx context.Context, in *im.IncomingMessage, reply *im.ReplyMessage) error {
	if reply != nil && strings.TrimSpace(reply.Content) == "NO_REPLY" {
		a.ExecutionFinished(ctx, in)
		return nil
	}
	if in == nil || reply == nil || strings.TrimSpace(reply.Content) == "" {
		return errors.New("empty Octo reply")
	}
	// No partial/thinking output: streaming capability is added separately.
	if reply.IsStreaming && !reply.IsFinal {
		return nil
	}
	if a.db != nil {
		scope, scopeErr := a.AuthorizeExecution(ctx, nil, in)
		var saved Inbox
		savedErr := a.inboxQuery(ctx, in.MessageID).Select("authority").First(&saved).Error
		if scopeErr != nil || savedErr != nil || !permitsReply(saved.Authority, scope) {
			_ = a.inboxQuery(ctx, in.MessageID).Updates(map[string]any{"state": "ignored", "error_code": "authorization_changed"}).Error
			return im.ErrScopeDenied
		}
		if err := a.inboxQuery(ctx, in.MessageID).Updates(map[string]any{"state": "reply_pending", "reply": reply.Content, "attempts": gorm.Expr("attempts + 1"), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	if err != nil || (kind != 1 && kind != 2 && kind != 5) {
		return errors.New("missing Octo reply target")
	}
	target := in.Extra["octo_channel_id"]
	if kind == 1 {
		target = in.UserID
	}
	if _, err = wire.ParseScope(target, byte(kind)); err != nil {
		return err
	}
	id := in.Extra["octo_message_id"]
	if !wire.DecimalID(id) {
		return errors.New("missing original Octo message ID")
	}
	text := reply.Content
	payload := map[string]any{"type": 1}
	if kind != 1 {
		label := "@" + in.UserName
		if in.UserName == "" {
			label = "@" + in.UserID
		}
		text = label + " " + text
		payload["mention"] = map[string]any{"uids": []string{in.UserID}, "entities": []wire.Entity{{UID: in.UserID, Offset: 0, Length: len(utf16.Encode([]rune(label)))}}}
	}
	a.contactEntities(ctx, in, text, payload)
	payload["content"] = text
	quote := map[string]any{"message_id": id, "from_uid": in.UserID, "from_name": in.UserName}
	var original map[string]json.RawMessage
	if json.Unmarshal([]byte(in.Extra["octo_original_payload"]), &original) == nil {
		quote["payload"] = original
	}
	payload["reply"] = quote
	// Models cannot redirect replies or inject mention-all through ReplyMessage.Extra.
	var result struct {
		ID json.RawMessage `json:"message_id"`
	}
	if err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{"channel_id": target, "channel_type": kind, "client_msg_no": uuid.NewSHA1(uuid.NameSpaceOID, []byte(in.MessageID+":reply")).String(), "payload": payload}, &result); err != nil {
		if a.db != nil {
			_ = a.inboxQuery(ctx, in.MessageID).Update("error_code", "send_failed").Error
		}
		return err
	}
	if wire.ReadID(result.ID) == "" {
		if a.db != nil {
			_ = a.inboxQuery(ctx, in.MessageID).Update("error_code", "delivery_not_acknowledged").Error
		}
		return errors.New("Octo delivery was not acknowledged")
	}
	if a.db != nil {
		return a.inboxQuery(ctx, in.MessageID).Updates(map[string]any{"state": "delivered", "error_code": "", "updated_at": time.Now()}).Error
	}
	return nil
}

func (a *Adapter) DownloadFile(ctx context.Context, msg *im.IncomingMessage) (io.ReadCloser, string, error) {
	if msg == nil {
		return nil, "", errors.New("missing Octo file")
	}
	u, err := url.Parse(msg.FileKey)
	if err != nil || u.Scheme != "https" || u.Hostname() != "cdn.deepminer.com.cn" || (u.Port() != "" && u.Port() != "443") || u.User != nil {
		return nil, "", errors.New("untrusted Octo attachment URL")
	}
	config := utils.DefaultSSRFSafeHTTPClientConfig()
	config.Timeout = 30 * time.Second
	client := utils.NewSSRFSafeHTTPClient(config)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errors.New("Octo attachment unavailable")
	}
	const maxFile = 20 << 20
	if resp.StatusCode != 200 || resp.ContentLength > maxFile {
		resp.Body.Close()
		return nil, "", fmt.Errorf("Octo attachment rejected (HTTP %d)", resp.StatusCode)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil || len(data) > maxFile {
		return nil, "", errors.New("Octo attachment exceeds limit or download failed")
	}
	return io.NopCloser(bytes.NewReader(data)), msg.FileName, nil
}
