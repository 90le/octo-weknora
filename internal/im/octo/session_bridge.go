package octo

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/google/uuid"
)

// SendSessionBridgeReply sends a Bot message back along the exact return path
// of an Octo session (DM / group / subarea), optionally quoting the latest
// user Octo message stored in octo_message_inbox.input when available.
func (a *Adapter) SendSessionBridgeReply(
	ctx context.Context, tenant uint64, targetChannelID string, channelType int, body string, quoteInboxInput string,
) (string, error) {
	if a.db == nil || a.tenantID != tenant {
		return "", errors.New("octo channel mismatch")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", errors.New("empty reply body")
	}
	if targetChannelID == "" || (channelType != 1 && channelType != 2 && channelType != 5) {
		return "", errors.New("invalid octo return target")
	}

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var payload map[string]any
	if strings.TrimSpace(quoteInboxInput) != "" {
		var in im.IncomingMessage
		if json.Unmarshal([]byte(quoteInboxInput), &in) == nil && in.UserID != "" {
			if p, _ := nativeReplyPayload(&in, channelType, body); p != nil {
				payload = p
			}
		}
	}
	if payload == nil {
		payload = map[string]any{
			"type":    1,
			"content": body,
		}
	}

	var response struct {
		ID json.RawMessage `json:"message_id"`
	}
	clientNo := "web:" + uuid.NewString()
	err := a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
		"channel_id":    targetChannelID,
		"channel_type":  channelType,
		"client_msg_no": clientNo,
		"payload":       payload,
	}, &response)
	if err != nil {
		return "", err
	}
	return wire.ReadID(response.ID), nil
}

// ParseInboxReturnTarget extracts exact channel_type, target channel_id, group,
// subarea, user and quote envelope from a stored octo_message_inbox.input JSON.
func ParseInboxReturnTarget(rawInput string) (*im.IncomingMessage, int, string, string, string, bool) {
	var in im.IncomingMessage
	if json.Unmarshal([]byte(rawInput), &in) != nil || in.UserID == "" {
		return nil, 0, "", "", "", false
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	if err != nil || (kind != 1 && kind != 2 && kind != 5) {
		if in.ChatType == im.ChatTypeDirect {
			kind = 1
		} else {
			return nil, 0, "", "", "", false
		}
	}
	targetID := strings.TrimSpace(in.Extra["octo_channel_id"])
	if kind == 1 {
		targetID = in.UserID
	}
	if targetID == "" {
		return nil, 0, "", "", "", false
	}
	groupID := strings.TrimSpace(in.Extra["octo_group_id"])
	subareaID := strings.TrimSpace(in.Extra["octo_subarea_id"])
	if groupID == "" && (kind == 2 || kind == 5) {
		if sc, scErr := wire.ParseScope(targetID, byte(kind)); scErr == nil {
			groupID, subareaID = sc.Group, sc.Subarea
		}
	}
	return &in, kind, targetID, groupID, subareaID, true
}
