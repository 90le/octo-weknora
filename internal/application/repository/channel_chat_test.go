package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelChatAuthorizationAndProjection(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, q := range []string{
		`CREATE TABLE im_channel_web_owners (tenant_id integer, im_channel_id text,` +
			` web_user_id text, deleted_at datetime)`,
		`CREATE TABLE im_channels (id text, tenant_id integer, platform text, deleted_at datetime)`,
		`CREATE TABLE tenant_members (tenant_id integer, user_id text, status text, deleted_at datetime)`,
		`CREATE TABLE users (id text, is_active boolean, deleted_at datetime)`,
		`INSERT INTO im_channels VALUES ('channel',7,'octo',NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL),('bob',1,NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES (7,'channel','alice',NULL)`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	ctx := context.Background()
	repo := NewMessageRepository(db)
	read := repo.(interface {
		ReadChannelChat(
			context.Context, uint64, string, string, int, *time.Time, string,
		) (*types.Session, []*types.Message, error)
	})
	id := createSession(t, db, &types.Session{TenantID: 7, Title: "chat", HostWorkspaceDir: "secret-path"})
	mapping := testIMChannelSession{
		ID: "mapping", SessionID: id, TenantID: 7, IMChannelID: "channel", Platform: "octo", ChatID: "group",
	}
	require.NoError(t, db.Create(&mapping).Error)
	s, messages, err := read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
	require.NoError(t, err)
	require.Equal(t, id, s.ID)
	require.Empty(t, messages)
	_, err = repo.CreateMessage(ctx, &types.Message{SessionID: id, Role: "system", Content: "system-secret"})
	require.NoError(t, err)
	_, err = repo.CreateMessage(ctx, &types.Message{SessionID: id, Role: "user", Content: "hello"})
	require.NoError(t, err)
	_, err = repo.CreateMessage(ctx, &types.Message{
		SessionID: id, Role: "assistant", Content: "answer", RenderedContent: "internal-secret",
		Artifacts: types.MessageArtifacts{testArtifact("report.txt", "secret-path", time.Now())},
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE messages SET created_at = ? WHERE session_id = ? AND role = ?",
		time.Now().UTC().Add(-time.Hour), id, "user").Error)
	s, messages, err = read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[1].Artifacts, 1)
	require.Equal(t, "report.txt", messages[1].Artifacts[0].FileName)
	require.Empty(t, messages[1].Artifacts[0].URL)
	require.Empty(t, messages[1].Artifacts[0].SourcePath)
	encoded, err := json.Marshal([]any{s, messages})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	const imageHandle = "resource://AbCdEfGhIjKlMnOpQrStUv"
	require.NoError(t, db.Exec("UPDATE message_artifacts SET url = ? WHERE session_id = ?", imageHandle, id).Error)
	require.NoError(t, db.Exec("UPDATE messages SET content = ? WHERE session_id = ? AND role = ?",
		"![image]("+imageHandle+")", id, "assistant").Error)
	_, messages, err = read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
	require.NoError(t, err)
	require.Equal(t, imageHandle, messages[1].Artifacts[0].URL)
	require.NoError(t, db.Exec("UPDATE messages SET content = ? WHERE session_id = ? AND role = ?",
		"unrelated", id, "assistant").Error)
	_, messages, err = read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
	require.NoError(t, err)
	require.Empty(t, messages[1].Artifacts[0].URL)
	_, page, err := read.ReadChannelChat(ctx, 7, "alice", id, 1, nil, "")
	require.NoError(t, err)
	require.Len(t, page, 1)
	before := page[0].CreatedAt
	_, page, err = read.ReadChannelChat(ctx, 7, "alice", id, 1, &before, "")
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "user", page[0].Role)
	// Stable cursor must not skip the other row when timestamps are equal.
	sameTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	require.NoError(t, db.Exec("UPDATE messages SET created_at = ? WHERE session_id = ? AND role IN ?",
		sameTime, id, []string{"user", "assistant"}).Error)
	_, page, err = read.ReadChannelChat(ctx, 7, "alice", id, 1, nil, "")
	require.NoError(t, err)
	require.Len(t, page, 1)
	firstID := page[0].ID
	before = page[0].CreatedAt
	_, page, err = read.ReadChannelChat(ctx, 7, "alice", id, 1, &before, firstID)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.NotEqual(t, firstID, page[0].ID)
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
		{`UPDATE im_channels SET platform='other'`, `UPDATE im_channels SET platform='octo'`},
	} {
		require.NoError(t, db.Exec(pair[0]).Error)
		_, _, err = read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		require.NoError(t, db.Exec(pair[1]).Error)
	}
	for _, args := range []struct {
		tenant uint64
		user   string
	}{{8, "alice"}, {7, "bob"}, {0, "alice"}, {7, ""}} {
		_, _, err = read.ReadChannelChat(ctx, args.tenant, args.user, id, 20, nil, "")
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	other := testIMChannelSession{ID: "other", SessionID: id, TenantID: 8, IMChannelID: "other", Platform: "octo"}
	require.NoError(t, db.Create(&other).Error)
	require.NoError(t, db.Delete(&other).Error)
	_, _, err = read.ReadChannelChat(ctx, 7, "alice", id, 20, nil, "")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestChannelArtifactHandleInContent(t *testing.T) {
	const handle = "resource://AbCdEfGhIjKlMnOpQrStUv"
	for _, tc := range []struct {
		ref, content string
		want         bool
	}{
		{handle, "![image](" + handle + ")", true},
		{handle, "![image](" + handle + "x)", false},
		{handle, "resource://AnotherHandle1234567890", false},
		{"local://secret/path", "local://secret/path", false},
		{" " + handle, handle, false},
		{handle + "x", handle + "x", false},
	} {
		require.Equal(t, tc.want, channelArtifactHandleInContent(tc.ref, tc.content), tc.ref)
	}
}
