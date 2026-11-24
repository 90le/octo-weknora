package octo

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
	"github.com/Tencent/WeKnora/internal/octobusiness"
)

// DeliverManualReply quotes the saved question with its native identity. Its
// independent client key leaves the original automated delivery intact.
func (a *Adapter) DeliverManualReply(
	ctx context.Context, target octobusiness.ManualReplyTarget, id, body string,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	in, kind, channel, err := a.checkManualReplyTarget(ctx, target)
	if err != nil {
		return "", err
	}
	payload, _ := nativeReplyPayload(in, kind, body)
	var response struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
		"channel_id": channel, "channel_type": kind, "client_msg_no": id, "payload": payload,
	}, &response)
	return wire.ReadID(response.ID), err
}

// ManualReplyRecipientName resolves the display name of the authorized original sender.
func (a *Adapter) ManualReplyRecipientName(ctx context.Context, target octobusiness.ManualReplyTarget) (string, error) {
	in, _, _, err := a.checkManualReplyTarget(ctx, target)
	if err != nil {
		return "", err
	}
	return in.UserName, nil
}

func (a *Adapter) checkManualReplyTarget(
	ctx context.Context, target octobusiness.ManualReplyTarget,
) (*im.IncomingMessage, int, string, error) {
	if a.db == nil || a.tenantID != target.TenantID || a.channelID != target.ChannelID ||
		!strings.HasPrefix(target.InboxID, "octo:"+a.uid+":") {
		return nil, 0, "", octobusiness.ErrDenied
	}
	var row Inbox
	var in im.IncomingMessage
	if a.inboxQuery(ctx, target.InboxID).Take(&row).Error != nil ||
		octobusiness.ManualSourceDigest(row.Input, row.Authority) != target.SourceDigest ||
		json.Unmarshal([]byte(row.Input), &in) != nil || in.UserID == "" ||
		in.Extra["octo_card_event_key"] != "" || !wire.DecimalID(in.Extra["octo_message_id"]) {
		return nil, 0, "", octobusiness.ErrDenied
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	channel := in.Extra["octo_channel_id"]
	if err != nil || (kind != 1 && kind != 2 && kind != 5) || target.IsDirect != (kind == 1) {
		return nil, 0, "", octobusiness.ErrDenied
	}
	if target.IsDirect {
		if in.ChatType != im.ChatTypeDirect || target.ScopeID != "dm:"+in.UserID {
			return nil, 0, "", octobusiness.ErrDenied
		}
		channel = in.UserID
	}
	if _, err = wire.ParseScope(channel, byte(kind)); err != nil {
		return nil, 0, "", octobusiness.ErrDenied
	}
	scope, err := a.AuthorizeExecution(ctx, nil, &in)
	if err != nil || scope == nil || scope.AccountID != target.AccountID || scope.ScopeID != target.ScopeID ||
		!slices.Contains(scope.KnowledgeBaseIDs, target.LibraryID) {
		return nil, 0, "", octobusiness.ErrDenied
	}
	// The retained input can predate nickname resolution. The current scope
	// belongs to this sender; some group rosters supply only a UID.
	name := scope.SenderName
	if !target.IsDirect && (name == "" || name == in.UserID) {
		name = a.profileDisplayName(ctx, in.UserID)
	}
	if name != "" && name != in.UserID {
		in.UserName = name
	}
	return &in, kind, channel, nil
}
