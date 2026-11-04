package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestPostgresChannelConversation uses only the dedicated local disposable
// database and rolls back all fixture schema, migration and data changes.
func TestPostgresChannelConversation(t *testing.T) {
	dsn := os.Getenv("WEKNORA_OCTO_ARTIFACT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_OCTO_ARTIFACT_TEST_POSTGRES_DSN to the disposable local database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Equal(t, "weknora_test_octo_artifact_delete", config.Database)
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, config.Host)
	for _, fallback := range config.Fallbacks {
		require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, fallback.Host)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var current string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&current).Error)
	require.Equal(t, "weknora_test_octo_artifact_delete", current)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { require.NoError(t, tx.Rollback().Error) })
	schema := "octo_conversation_" + uuid.NewString()[:8]
	require.NoError(t, tx.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	require.NoError(t, tx.Exec(`SET LOCAL search_path TO "`+schema+`"`).Error)
	for _, statement := range []string{
		`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id BIGINT, platform TEXT, ` +
			`credentials JSONB, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE im_channel_sessions(id TEXT PRIMARY KEY, session_id TEXT, tenant_id BIGINT, ` +
			`im_channel_id TEXT, platform TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE audit_logs(id BIGSERIAL PRIMARY KEY, tenant_id BIGINT, action TEXT, target_type TEXT, ` +
			`target_id TEXT, target_user_id TEXT, outcome TEXT)`,
		`CREATE TABLE tenant_members(tenant_id BIGINT, user_id TEXT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY, tenant_id BIGINT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE messages(id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, ` +
			`created_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE message_artifacts(session_id TEXT, message_id TEXT, position INTEGER, deleted_at TIMESTAMPTZ)`,
		`CREATE FUNCTION uuid_generate_v4() RETURNS uuid LANGUAGE sql AS $$ ` +
			`SELECT '00000000-0000-4000-8000-000000000001'::uuid $$`,
		`INSERT INTO im_channels VALUES ('channel',7,'octo','{"account_id":"a","bot_uid":"b"}',NULL)`,
		`INSERT INTO im_channel_sessions VALUES ('mapping','session',7,'channel','octo',NULL)`,
		`INSERT INTO tenant_members VALUES (7,'alice','active',NULL),(7,'bob','active',NULL)`,
		`INSERT INTO users VALUES ('alice',true,NULL),('bob',true,NULL)`,
		`INSERT INTO sessions VALUES ('session',7,NULL)`,
		`INSERT INTO messages SELECT 'q' || i, 'session', 'user', 'question', now(), NULL FROM generate_series(1,50) i`,
		`INSERT INTO messages VALUES ('anchor','session','assistant','answer',now()+interval '1 second',NULL)`,
		`INSERT INTO message_artifacts VALUES ('session','anchor',0,NULL)`,
	} {
		require.NoError(t, tx.Exec(statement).Error)
	}
	migration, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "migrations", "versioned", "000134_im_channel_web_owners.up.sql",
	))
	require.NoError(t, err)
	require.NoError(t, tx.Exec(string(migration)).Error)
	require.NoError(t, tx.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id) `+
		`VALUES ('owner',7,'channel','alice')`).Error)
	for _, statement := range []string{
		`ALTER TABLE sessions ADD COLUMN title TEXT, ADD COLUMN created_at TIMESTAMPTZ, ` +
			`ADD COLUMN updated_at TIMESTAMPTZ`,
		`ALTER TABLE messages ADD COLUMN request_id TEXT, ADD COLUMN is_completed BOOLEAN, ` +
			`ADD COLUMN is_fallback BOOLEAN, ADD COLUMN channel TEXT, ADD COLUMN updated_at TIMESTAMPTZ, ` +
			`ADD COLUMN knowledge_references JSON`,
		`ALTER TABLE message_artifacts ADD COLUMN url TEXT, ADD COLUMN file_name TEXT, ADD COLUMN file_type TEXT, ` +
			`ADD COLUMN file_size BIGINT, ADD COLUMN created_at TIMESTAMPTZ`,
		`UPDATE sessions SET title = 'chat', created_at = now(), updated_at = now()`,
		`UPDATE messages SET knowledge_references = '[]'::json, is_completed = true, is_fallback = false, ` +
			`request_id = '', channel = 'im', updated_at = now()`,
		`UPDATE message_artifacts SET url = '', file_name = 'report', file_type = '.txt', ` +
			`file_size = 1, created_at = now()`,
	} {
		require.NoError(t, tx.Exec(statement).Error)
	}
	chat := NewMessageRepository(tx).(interface {
		ReadChannelChat(
			context.Context, uint64, string, string, int, *time.Time, string,
		) (*types.Session, []*types.Message, error)
	})
	_, latest, err := chat.ReadChannelChat(context.Background(), 7, "alice", "session", 2, nil, "")
	require.NoError(t, err)
	require.Len(t, latest, 2)
	cursorTime, cursorID := latest[0].CreatedAt, latest[0].ID
	_, previous, err := chat.ReadChannelChat(context.Background(), 7, "alice", "session", 2, &cursorTime, cursorID)
	require.NoError(t, err)
	require.Len(t, previous, 2)
	require.NotEqual(t, cursorID, previous[len(previous)-1].ID)
	_, _, err = chat.ReadChannelChat(context.Background(), 7, "bob", "session", 2, nil, "")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	repo := NewMessageRepository(tx).(interface {
		ReadChannelConversation(
			context.Context, uint64, string, string, string, int, int,
		) ([]types.ChannelConversationMessage, int, bool, error)
	})
	ctx := context.Background()
	read := func(user string, page int) ([]types.ChannelConversationMessage, int, bool, error) {
		return repo.ReadChannelConversation(ctx, 7, user, "session", "anchor", 0, page)
	}
	rows, page, more, err := read("alice", 0)
	require.NoError(t, err)
	require.Equal(t, 2, page)
	require.Len(t, rows, 1)
	require.Equal(t, "anchor", rows[0].ID)
	require.False(t, more)
	rows, _, more, err = read("alice", 1)
	require.NoError(t, err)
	require.Len(t, rows, 50)
	require.True(t, more)
	_, _, _, err = read("bob", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, tx.Exec(`INSERT INTO im_channel_sessions VALUES `+
		`('ambiguous','session',7,'other-channel','octo',NULL)`).Error)
	_, _, _, err = read("alice", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	rows, _, _, err = read("alice", 1)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, tx.Exec(`UPDATE im_channel_sessions SET deleted_at=now() WHERE id='ambiguous'`).Error)
	_, _, _, err = read("alice", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, tx.Exec(`DELETE FROM im_channel_sessions WHERE id='ambiguous'`).Error)
	require.NoError(t, tx.Exec(`UPDATE messages SET deleted_at = now() WHERE id = 'q1'`).Error)
	rows, _, more, err = read("alice", 1)
	require.NoError(t, err)
	require.Len(t, rows, 50)
	require.False(t, more)
	require.NoError(t, tx.Exec(`UPDATE im_channel_web_owners SET deleted_at = now() WHERE id = 'owner'`).Error)
	_, _, _, err = read("alice", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, tx.Exec(`UPDATE im_channel_web_owners SET deleted_at = NULL WHERE id = 'owner'`).Error)
	require.NoError(t, tx.Exec(`UPDATE im_channels SET credentials = '{"account_id":"new","bot_uid":"b"}' `+
		`WHERE id = 'channel'`).Error)
	require.NoError(t, tx.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id) `+
		`VALUES ('owner2',7,'channel','alice')`).Error)
	_, _, _, err = read("alice", 0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
