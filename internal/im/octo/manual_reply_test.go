package octo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/im/octo/wire"
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
		Extra: map[string]string{
			"octo_channel_type": "1", "octo_channel_id": user, "octo_message_id": originalID,
			"octo_original_payload": `{"type":1,"content":"Direct question"}`,
		},
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
	_, err = a.ManualReplyRecipientName(ctx, target)
	require.NoError(t, err)
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

	for _, tc := range []struct{ mid, original string }{
		{"99", `{"type":1,"content":"Another message"}`},
		{originalID, ""},
	} {
		m.Extra["octo_message_id"], m.Extra["octo_original_payload"] = tc.mid, tc.original
		changed, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, a.inboxQuery(ctx, id).Update("input", string(changed)).Error)
		target.SourceDigest = octobusiness.ManualSourceDigest(string(changed), entry.Authority)
		_, err = a.DeliverManualReply(ctx, target, "claim", "Reply")
		require.ErrorIs(t, err, octobusiness.ErrDenied)
		require.Empty(t, requests)
	}
}

func TestManualGroupReplyRecipientName(t *testing.T) {
	const uid, channel, mid = "feedback-author", "group", "2098355867442221056"
	for _, tc := range []struct {
		test, memberName, profileUID, want string
	}{
		{"member nickname", "小林🌟", "", "小林🌟"},
		{"UID-only roster uses profile", uid, uid, "阿文🌙"},
		{"mismatched profile keeps original identity", "", "another-user", uid},
	} {
		t.Run(tc.test, func(t *testing.T) {
			a, _ := scheduledAdapter(t)
			ctx := context.Background()
			require.NoError(t, a.db.AutoMigrate(&Inbox{}))
			msg := im.IncomingMessage{
				UserID: uid, UserName: uid, ChatType: im.ChatTypeGroup,
				Extra: map[string]string{
					"octo_channel_type": "2", "octo_channel_id": channel, "octo_message_id": mid,
					"octo_original_payload": `{"type":1,"content":"提交的反馈"}`,
				},
			}
			input, err := json.Marshal(msg)
			require.NoError(t, err)
			scope := im.ExecutionScope{
				AccountID: "account", ScopeID: "scope", KnowledgeBaseIDs: []string{"kb"}, SenderName: tc.memberName,
			}
			row := Inbox{
				TenantID: a.tenantID, ChannelID: a.channelID, MessageID: "octo:" + a.uid + ":" + mid,
				Input: string(input), Authority: encodeAuthority(&scope),
			}
			require.NoError(t, a.db.Create(&row).Error)
			a.policy = func(_ context.Context, _ *im.IMChannel, in *im.IncomingMessage) (*im.ExecutionScope, error) {
				require.Equal(t, uid, in.UserID)
				return &scope, nil
			}
			target := octobusiness.ManualReplyTarget{
				TenantID: a.tenantID, ChannelID: a.channelID, AccountID: scope.AccountID, ScopeID: scope.ScopeID,
				InboxID: row.MessageID, LibraryID: "kb",
				SourceDigest: octobusiness.ManualSourceDigest(row.Input, row.Authority),
			}
			sent := make(chan wire.Payload, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/bot/user/info":
					if tc.profileUID == "" || r.URL.Query().Get("uid") != uid {
						t.Error("unexpected profile lookup")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"uid": tc.profileUID, "name": "阿文🌙"})
				case "/v1/bot/sendMessage":
					var req struct {
						Channel string       `json:"channel_id"`
						Payload wire.Payload `json:"payload"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.Channel != channel {
						t.Error("invalid group delivery")
					}
					sent <- req.Payload
					_, _ = w.Write([]byte(`{"code":200,"data":{"message_id":"456"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			a.api.base, a.api.http = server.URL, server.Client()
			name, err := a.ManualReplyRecipientName(ctx, target)
			require.NoError(t, err)
			require.Equal(t, tc.want, name)
			require.Empty(t, sent)
			receipt, err := a.DeliverManualReply(ctx, target, "claim", "人工答复")
			require.NoError(t, err)
			require.Equal(t, "456", receipt)
			require.Len(t, sent, 1)
			payload := <-sent
			label := "@" + tc.want
			require.Equal(t, label+" 人工答复", payload.Text())
			require.Equal(t, []string{uid}, payload.Mention.UIDs)
			entity := wire.Entity{UID: uid, Length: len(utf16.Encode([]rune(label)))}
			require.Equal(t, []wire.Entity{entity}, payload.Mention.Entities)
			require.NotNil(t, payload.Reply)
			require.Equal(t, mid, wire.ReadID(payload.Reply.ID))
			require.Equal(t, uid, payload.Reply.Sender)
			require.Equal(t, tc.want, payload.Reply.Name)
			require.NotNil(t, payload.Reply.Payload)
			require.Equal(t, "提交的反馈", payload.Reply.Payload.Text())
		})
	}
}
