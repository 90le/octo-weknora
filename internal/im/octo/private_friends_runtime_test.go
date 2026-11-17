package octo

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/octointegration"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type privateFriendRoundTrip func(*http.Request) (*http.Response, error)

func (f privateFriendRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOctoFriendRevocationBlocksAnAlreadyAdmittedReply(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "private-friends.db")), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sql.Close() })
	require.NoError(t, db.AutoMigrate(&octointegration.Connection{}, &Inbox{}))
	for _, statement := range []string{
		`CREATE TABLE im_channels (id TEXT, tenant_id INTEGER, enabled BOOLEAN, credentials TEXT, ` +
			`knowledge_base_id TEXT, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE knowledge_bases (id TEXT, tenant_id INTEGER, deleted_at DATETIME)`,
		`INSERT INTO knowledge_bases VALUES ('kb', 1, NULL)`,
		`INSERT INTO im_channels VALUES ('channel', 1, 1, ` +
			`'{"account_id":"account","bot_uid":"bot","dm_access_mode":"creator_and_friends"}', ` +
			`'kb', CURRENT_TIMESTAMP, NULL)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.Create(&octointegration.Connection{
		TenantID: 1, AccountID: "account", Token: "snapshot",
		VerifiedIdentity: types.JSON(`{"bot_uid":"bot","owner_uid":"owner"}`),
	}).Error)
	a, err := NewAdapter("bf_fixture", "bot")
	require.NoError(t, err)
	a.db, a.channelID, a.tenantID = db, "channel", 1
	a.policy = runtimePolicy(db, a, "channel", 1, "account", "snapshot")
	friend := true
	var checks, sends int
	a.api.http.Transport = privateFriendRoundTrip(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{}`
		switch r.URL.Path {
		case "/api/v1/bot/messages/sync":
			checks++
			if friend {
				body = `{"start_message_seq":0,"end_message_seq":0,"messages":[]}`
			} else {
				status, body = http.StatusBadRequest, `{"error":{"code":"err.server.bot_api.not_friend"}}`
			}
		case "/api/v1/bot/user/info":
			body = `{"uid":"user","name":"User"}`
		case "/api/v1/bot/sendMessage":
			sends++
			body = `{"message_id":"42"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	in := &im.IncomingMessage{
		Platform: Platform, ChatType: im.ChatTypeDirect, UserID: "user", Content: "first question", MessageID: "first",
		Extra: map[string]string{"octo_channel_type": "1", "octo_channel_id": "user", "octo_message_id": "41"},
	}
	handled := 0
	err = a.accept(ctx, in, func(ctx context.Context, msg *im.IncomingMessage) error {
		handled++
		friend = false // Octo removed this same peer after admission.
		return a.SendReply(ctx, msg, &im.ReplyMessage{Content: "private result", IsFinal: true})
	})
	require.ErrorIs(t, err, im.ErrScopeDenied)
	require.Zero(t, sends)
	var stored Inbox
	require.NoError(t, a.inboxQuery(ctx, in.MessageID).First(&stored).Error)
	require.Equal(t, "ignored", stored.State)
	newTurn := *in
	newTurn.MessageID = "next"
	require.NoError(t, a.accept(ctx, &newTurn, func(context.Context, *im.IncomingMessage) error {
		handled++
		return nil
	}))
	require.Equal(t, 1, handled, "the removed friend cannot enter the reply handler again")
	require.Equal(t, 3, checks, "admission, reply and the next message each check Octo live")
	var count int64
	require.NoError(t, a.inboxQuery(ctx, newTurn.MessageID).Count(&count).Error)
	require.Zero(t, count, "denied messages are not retained")
}
