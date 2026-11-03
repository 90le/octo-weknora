package im

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestOctoSessionRejectsStaleAdapterIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sessions.db")), &gorm.Config{})
	require.NoError(t, err)
	for _, sql := range []string{
		`CREATE TABLE im_channel_sessions (id TEXT PRIMARY KEY, platform TEXT, user_id TEXT, chat_id TEXT,
 thread_id TEXT, session_id TEXT, tenant_id INTEGER, agent_id TEXT, im_channel_id TEXT, status TEXT,
 metadata TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id INTEGER, platform TEXT,
 credentials TEXT, deleted_at DATETIME)`,
		`CREATE TABLE audit_logs(id INTEGER PRIMARY KEY, tenant_id INTEGER, action TEXT, target_type TEXT,
 target_id TEXT, target_user_id TEXT, outcome TEXT)`,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY, deleted_at DATETIME)`,
		`INSERT INTO im_channels VALUES ('channel',1,'octo','{"account_id":"a","bot_uid":"b"}',NULL)`,
	} {
		require.NoError(t, db.Exec(sql).Error)
	}
	migration, err := os.ReadFile("../../migrations/sqlite/000052_im_channel_web_owners.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	svc := &Service{db: db, sessionService: &octoSessionService{}}
	msg := &IncomingMessage{
		Platform: "octo", UserID: "user", ChatID: "chat",
		octoIdentity: []byte(`{"account_id":"a","bot_uid":"b"}`),
	}
	first, err := svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.NoError(t, err)
	again, err := svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.NoError(t, err)
	require.Equal(t, first.SessionID, again.SessionID)
	require.NoError(t, db.Exec(`UPDATE im_channels SET credentials='{"account_id":"new","bot_uid":"b"}'`).Error)
	_, err = svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.ErrorIs(t, err, ErrScopeDenied)
	msg.octoIdentity = []byte(`{"account_id":"new","bot_uid":"b"}`)
	require.NoError(t, db.Table("im_channels").Select("owner_identity_generation").
		Where("id = ?", "channel").Scan(&msg.octoIdentityGeneration).Error)
	next, err := svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, next.SessionID)
	require.NoError(t, db.Exec(`UPDATE im_channels SET credentials='{"account_id":"a","bot_uid":"b"}'`).Error)
	msg.octoIdentity = []byte(`{"account_id":"a","bot_uid":"b"}`)
	msg.octoIdentityGeneration = ""
	_, err = svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.ErrorIs(t, err, ErrScopeDenied)
	require.NoError(t, db.Table("im_channels").Select("owner_identity_generation").
		Where("id = ?", "channel").Scan(&msg.octoIdentityGeneration).Error)
	third, err := svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, third.SessionID)
	oldGeneration := msg.octoIdentityGeneration
	require.NoError(t, db.Exec(`DELETE FROM im_channels WHERE id='channel'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO im_channels(id,tenant_id,platform,credentials)
 VALUES ('channel',1,'octo','{"account_id":"a","bot_uid":"b"}')`).Error)
	_, err = svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.ErrorIs(t, err, ErrScopeDenied)
	require.NoError(t, db.Table("im_channels").Select("owner_identity_generation").
		Where("id = ?", "channel").Scan(&msg.octoIdentityGeneration).Error)
	require.NotEqual(t, oldGeneration, msg.octoIdentityGeneration)
	_, err = svc.resolveUserSession(context.Background(), msg, 1, "agent", "channel")
	require.NoError(t, err)
}
