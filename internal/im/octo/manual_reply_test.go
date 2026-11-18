package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octobusiness"
	"github.com/stretchr/testify/require"
)

func TestManualDirectReplyChecksCurrentPolicyAfterPreview(t *testing.T) {
	a, _ := scheduledAdapter(t)
	ctx := context.Background()
	require.NoError(t, a.db.AutoMigrate(&Inbox{}))
	const originalID = "2098355867442221056"
	const user = "direct-user"
	id := "octo:" + a.uid + ":" + originalID
	m := im.IncomingMessage{
		MessageID: id, UserID: user, ChatType: im.ChatTypeDirect,
		Extra: map[string]string{"octo_channel_type": "1", "octo_channel_id": user, "octo_message_id": originalID},
	}
	input, err := json.Marshal(m)
	require.NoError(t, err)
	scope := im.ExecutionScope{AccountID: "account", ScopeID: "dm:" + user, KnowledgeBaseIDs: []string{"kb"}}
	entry := Inbox{
		TenantID: a.tenantID, ChannelID: a.channelID, MessageID: id, Input: string(input),
		Authority: encodeAuthority(&scope), State: "delivered",
	}
	require.NoError(t, a.db.Create(&entry).Error)
	allowed := true
	a.policy = func(context.Context, *im.IMChannel, *im.IncomingMessage) (*im.ExecutionScope, error) {
		if !allowed {
			return nil, im.ErrScopeDenied
		}
		return &scope, nil
	}
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path != "/v1/bot/sendMessage" || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		_, _ = w.Write([]byte(`{"code":200,"data":{"message_id":"2098355867442221057"}}`))
	}))
	defer server.Close()
	a.api.base = server.URL
	a.api.http = server.Client()
	target := octobusiness.ManualReplyTarget{
		TenantID: a.tenantID, ChannelID: a.channelID, AccountID: scope.AccountID, ScopeID: scope.ScopeID,
		InboxID: id, LibraryID: "kb", IsDirect: true,
		SourceDigest: octobusiness.ManualSourceDigest(entry.Input, entry.Authority),
	}
	require.NoError(t, a.CheckManualReply(ctx, target))
	require.Empty(t, requests)
	allowed = false
	_, err = a.DeliverManualReply(ctx, target, "claim", "Reply")
	require.ErrorIs(t, err, octobusiness.ErrDenied)
	require.Empty(t, requests)
	allowed = true
	receipt, err := a.DeliverManualReply(ctx, target, "claim", "Reply")
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	body := <-requests
	require.Equal(t, user, body["channel_id"])
	payload := body["payload"].(map[string]any)
	require.Equal(t, originalID, payload["reply"].(map[string]any)["message_id"])
	require.NotContains(t, payload, "mention")
}
