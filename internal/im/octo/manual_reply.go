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
	if a.db == nil || a.tenantID != target.TenantID || a.channelID != target.ChannelID ||
		!strings.HasPrefix(target.InboxID, "octo:"+a.uid+":") {
		return "", octobusiness.ErrDenied
	}
	var row Inbox
	var in im.IncomingMessage
	if a.inboxQuery(ctx, target.InboxID).Take(&row).Error != nil ||
		octobusiness.ManualSourceDigest(row.Input, row.Authority) != target.SourceDigest ||
		json.Unmarshal([]byte(row.Input), &in) != nil || in.UserID == "" ||
		in.Extra["octo_card_event_key"] != "" || !wire.DecimalID(in.Extra["octo_message_id"]) {
		return "", octobusiness.ErrDenied
	}
	kind, err := strconv.Atoi(in.Extra["octo_channel_type"])
	channel := in.Extra["octo_channel_id"]
	if err != nil || (kind != 2 && kind != 5) {
		return "", octobusiness.ErrDenied
	}
	if _, err = wire.ParseScope(channel, byte(kind)); err != nil {
		return "", octobusiness.ErrDenied
	}
	scope, err := a.AuthorizeExecution(ctx, nil, &in)
	if err != nil || scope == nil || scope.AccountID != target.AccountID || scope.ScopeID != target.ScopeID ||
		!slices.Contains(scope.KnowledgeBaseIDs, target.LibraryID) {
		return "", octobusiness.ErrDenied
	}
	payload, _ := nativeReplyPayload(&in, kind, body)
	var response struct {
		ID json.RawMessage `json:"message_id"`
	}
	err = a.api.post(ctx, "/v1/bot/sendMessage", map[string]any{
		"channel_id": channel, "channel_type": kind, "client_msg_no": id, "payload": payload,
	}, &response)
	return wire.ReadID(response.ID), err
}
