package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestChannelConversationAuthorization(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channel_web_owners (tenant_id integer, im_channel_id text,` +
			` web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('channel',7,NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'channel','alice',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	repo := NewMessageRepository(db)
	ctx := context.Background()
	sid := createSession(t, db, &types.Session{TenantID: 7})
	require.NoError(t, db.Create(&testIMChannelSession{
		ID: "mapping", SessionID: sid, TenantID: 7, IMChannelID: "channel", Platform: "octo",
	}).Error)
	_, err := repo.CreateMessage(ctx, &types.Message{SessionID: sid, Role: "user", Content: "question"})
	require.NoError(t, err)
	_, err = repo.CreateMessage(ctx, &types.Message{SessionID: sid, Role: "system", Content: "secret"})
	require.NoError(t, err)
	msg, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: sid, Role: "assistant", Content: "answer",
		Artifacts: types.MessageArtifacts{testArtifact("report.txt", "file", time.Now())},
	})
	require.NoError(t, err)
	read := repo.(interface {
		ReadChannelConversation(
			context.Context, uint64, string, string, string, int, int,
		) ([]types.ChannelConversationMessage, int, bool, error)
	})
	get := func(tenant uint64, user, session, anchor string, position int) []types.ChannelConversationMessage {
		rows, _, _, e := read.ReadChannelConversation(ctx, tenant, user, session, anchor, position, 1)
		require.NoError(t, e)
		return rows
	}
	rows := get(7, "alice", sid, msg.ID, 0)
	require.Len(t, rows, 2)
	require.NotContains(t, rows[0].Content, "secret")
	require.Empty(t, get(7, "bob", sid, msg.ID, 0))
	require.Empty(t, get(8, "alice", sid, msg.ID, 0))
	require.Empty(t, get(7, "alice", sid, msg.ID, 1))
	require.Empty(t, get(7, "alice", sid, "missing", 0))
	for _, mapping := range []testIMChannelSession{
		{ID: "other-chat", SessionID: sid, TenantID: 7, IMChannelID: "channel", Platform: "octo", ChatID: "other"},
		{ID: "other-channel", SessionID: sid, TenantID: 7, IMChannelID: "other", Platform: "octo"},
		{ID: "other-tenant", SessionID: sid, TenantID: 8, IMChannelID: "channel", Platform: "octo"},
	} {
		require.NoError(t, db.Create(&mapping).Error)
		require.Empty(t, get(7, "alice", sid, msg.ID, 0))
		_, _, _, e := read.ReadChannelConversation(ctx, 7, "alice", sid, msg.ID, 0, 0)
		require.Error(t, e)
		require.NoError(t, db.Delete(&mapping).Error)
		require.Empty(t, get(7, "alice", sid, msg.ID, 0)) // historical ambiguity also fails closed
		require.NoError(t, db.Unscoped().Delete(&mapping).Error)
	}
	for _, pair := range [][2]string{
		{`UPDATE users SET is_active=0 WHERE id='alice'`, `UPDATE users SET is_active=1 WHERE id='alice'`},
		{
			`UPDATE tenant_members SET status='inactive' WHERE user_id='alice'`,
			`UPDATE tenant_members SET status='active' WHERE user_id='alice'`,
		},
		{
			`UPDATE im_channel_web_owners SET deleted_at=CURRENT_TIMESTAMP`,
			`UPDATE im_channel_web_owners SET deleted_at=NULL`,
		},
		{`UPDATE im_channels SET deleted_at=CURRENT_TIMESTAMP`, `UPDATE im_channels SET deleted_at=NULL`},
		{`UPDATE im_channel_sessions SET im_channel_id=''`, `UPDATE im_channel_sessions SET im_channel_id='channel'`},
		{`UPDATE message_artifacts SET deleted_at=CURRENT_TIMESTAMP`, `UPDATE message_artifacts SET deleted_at=NULL`},
	} {
		require.NoError(t, db.Exec(pair[0]).Error)
		require.Empty(t, get(7, "alice", sid, msg.ID, 0))
		require.NoError(t, db.Exec(pair[1]).Error)
	}
	// The initial request must land on the page containing the artifact's
	// assistant message even when many earlier messages exist.
	require.NoError(t, db.Exec("UPDATE messages SET created_at = ? WHERE id = ?",
		time.Now().UTC().Add(24*time.Hour), msg.ID).Error)
	for range 55 {
		_, err = repo.CreateMessage(ctx, &types.Message{SessionID: sid, Role: "user", Content: "earlier"})
		require.NoError(t, err)
	}
	rows, page, more, err := read.ReadChannelConversation(ctx, 7, "alice", sid, msg.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 2, page)
	require.False(t, more)
	require.Equal(t, msg.ID, rows[len(rows)-1].ID)
	first, _, more, err := read.ReadChannelConversation(ctx, 7, "alice", sid, msg.ID, 0, 1)
	require.NoError(t, err)
	require.Len(t, first, 50)
	require.True(t, more)
	require.NoError(t, db.Exec("UPDATE messages SET deleted_at = ? WHERE id IN "+
		"(SELECT id FROM messages WHERE session_id = ? AND role = ? LIMIT 7)",
		time.Now().UTC(), sid, "user").Error)
	last, _, more, err := read.ReadChannelConversation(ctx, 7, "alice", sid, msg.ID, 0, 1)
	require.NoError(t, err)
	require.Len(t, last, 50)
	require.False(t, more)
	require.NoError(t, db.Exec(`UPDATE im_channel_web_owners SET deleted_at=CURRENT_TIMESTAMP`).Error)
	_, _, _, err = read.ReadChannelConversation(ctx, 7, "alice", sid, msg.ID, 0, 0)
	require.Error(t, err)
}
