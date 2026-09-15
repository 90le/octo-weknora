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
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const Platform im.Platform = "octo"

type Adapter struct {
	api    *apiClient
	uid    string
	policy func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error)
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
	if raw == nil || !wire.DecimalID(raw.ID) || raw.Sender == "" {
		return nil, errors.New("invalid Octo message")
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

func (a *Adapter) SendReply(ctx context.Context, in *im.IncomingMessage, reply *im.ReplyMessage) error {
	if in == nil || reply == nil || strings.TrimSpace(reply.Content) == "" {
		return errors.New("empty Octo reply")
	}
	// No partial/thinking output: streaming capability is added separately.
	if reply.IsStreaming && !reply.IsFinal {
		return nil
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
	if err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{"channel_id": target, "channel_type": kind, "client_msg_no": uuid.NewString(), "payload": payload}, &result); err != nil {
		return err
	}
	if wire.ReadID(result.ID) == "" {
		return errors.New("Octo delivery was not acknowledged")
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
