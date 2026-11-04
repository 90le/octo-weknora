package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDeleteChannelArtifactAuthorizationAndVersions(t *testing.T) {
	db := newArtifactTestDB(t)
	for _, sql := range []string{
		`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id INTEGER, ` +
			`platform TEXT, deleted_at DATETIME)`,
		`CREATE TABLE im_channel_web_owners(id TEXT PRIMARY KEY, tenant_id` +
			` INTEGER, im_channel_id TEXT, web_user_id TEXT, deleted_at DATETI` +
			`ME)`,
		`CREATE TABLE tenant_members(id TEXT PRIMARY KEY, tenant_id INTEGE` +
			`R, user_id TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN, deleted_at DATETIME)`,
		`CREATE TABLE auth_tokens(id TEXT PRIMARY KEY, user_id TEXT, token` +
			`_type TEXT, is_revoked BOOLEAN, expires_at DATETIME)`,
		`INSERT INTO im_channels VALUES ('channel',7,'octo',NULL)`,
		`INSERT INTO im_channel_web_owners VALUES ('owner',7,'channel','alice',NULL)`,
		`INSERT INTO tenant_members VALUES ('member',7,'alice','active',NULL)`,
		`INSERT INTO users VALUES ('alice',1,NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO auth_tokens VALUES ('token','alice','access_token',0,`+
		`?)`, time.Now().Add(time.Hour)).Error)
	sid := createSession(t, db, &types.Session{TenantID: 7})
	mapping := &testIMChannelSession{
		ID: "mapping", SessionID: sid, TenantID: 7, IMChannelID: "channel", Platform: "octo",
	}
	require.NoError(t, db.Create(mapping).Error)
	// A cleared chat still belongs to its channel.
	require.NoError(t, db.Delete(mapping).Error)
	repo := NewMessageRepository(db)
	ctx := context.Background()
	first, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: sid, Role: "assistant",
		Artifacts: types.MessageArtifacts{testArtifact("one.txt", "/same", time.Now())},
	})
	require.NoError(t, err)
	second, err := repo.CreateMessage(ctx, &types.Message{
		SessionID: sid, Role: "assistant",
		Artifacts: types.MessageArtifacts{testArtifact("two.txt", "/same", time.Now())},
	})
	require.NoError(t, err)
	deleter := repo.(interface {
		DeleteChannelArtifact(
			context.Context, uint64, string, string, time.Time, *types.ArtifactDeleteRequest,
		) ([]types.MessageArtifactRecord, []types.ArtifactRef, error)
	})
	req := &types.ArtifactDeleteRequest{SessionID: sid, MessageID: second.ID, Index: 0, AllVersions: true}
	for _, tc := range []struct {
		tenant      uint64
		user, token string
	}{{8, "alice", "token"}, {7, "bob", "token"}, {7, "alice", "missing"}} {
		_, _, err = deleter.DeleteChannelArtifact(ctx, tc.tenant, tc.user, tc.token, time.Now().Add(time.Hour), req)
		require.Error(t, err)
	}
	_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", time.Now().Add(-time.Second), req)
	require.Error(t, err)
	for _, pair := range [][2]string{
		{`UPDATE auth_tokens SET is_revoked = 1`, `UPDATE auth_tokens SET is_revoked = 0`},
		{`UPDATE tenant_members SET status = 'inactive'`, `UPDATE tenant_members SET status = 'active'`},
		{`UPDATE auth_tokens SET expires_at = '2000-01-01'`, `UPDATE auth_tokens SET expires_at = '2099-01-01'`},
		{
			`UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP`,
			`UPDATE im_channel_web_owners SET deleted_at = NULL`,
		},
		{
			`UPDATE im_channel_sessions SET im_channel_id = ''`,
			`UPDATE im_channel_sessions SET im_channel_id = 'channel'`,
		},
		{`UPDATE users SET is_active = 0`, `UPDATE users SET is_active = 1`},
		{`UPDATE sessions SET deleted_at = CURRENT_TIMESTAMP`, `UPDATE sessions SET deleted_at = NULL`},
	} {
		require.NoError(t, db.Exec(pair[0]).Error)
		_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", time.Now().Add(time.Hour), req)
		require.Error(t, err)
		require.NoError(t, db.Exec(pair[1]).Error)
	}
	bad := *req
	bad.MessageID = first.ID
	bad.Index = 5
	_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", time.Now().Add(time.Hour), &bad)
	require.Error(t, err)
	rows, marked, err := deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", time.Now().Add(time.Hour), req)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Len(t, marked, 2)
	_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", time.Now().Add(time.Hour), req)
	require.Error(t, err)
}
