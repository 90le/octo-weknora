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

// This test only accepts the explicitly named disposable database on the
// local host. All fixture DDL and DML run inside a transaction that is rolled
// back; no existing schema, migration state, or application data is touched.
// Set WEKNORA_OCTO_ARTIFACT_TEST_POSTGRES_DSN to the dedicated local database.
// Example database: weknora_test_octo_artifact_delete.
func TestPostgresDeleteChannelArtifact(t *testing.T) {
	dsn := os.Getenv("WEKNORA_OCTO_ARTIFACT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_OCTO_ARTIFACT_TEST_POSTGRES_DSN to a disposable local database")
	}
	config, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Equal(t, "weknora_test_octo_artifact_delete", config.Database,
		"refusing a database that is not the dedicated test database")
	require.Contains(t, []string{"localhost", "127.0.0.1", "::1"}, config.Host, "refusing a non-local PostgreSQL host")
	// No failover host may bypass the local-host check.
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
	schema := "octo_artifact_" + uuid.NewString()[:8]
	require.NoError(t, tx.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	require.NoError(t, tx.Exec(`SET LOCAL search_path TO "`+schema+`"`).Error)
	for _, statement := range []string{
		`CREATE TABLE im_channels(id TEXT PRIMARY KEY, tenant_id BIGINT, p` +
			`latform TEXT, credentials JSONB, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE im_channel_sessions(id TEXT PRIMARY KEY, session_id ` +
			`TEXT, tenant_id BIGINT, im_channel_id TEXT, platform TEXT, delete` +
			`d_at TIMESTAMPTZ)`,
		`CREATE TABLE audit_logs(id BIGSERIAL PRIMARY KEY, tenant_id BIGIN` +
			`T, action TEXT, target_type TEXT, target_id TEXT, target_user_id ` +
			`TEXT, outcome TEXT)`,
		`CREATE TABLE tenant_members(id BIGINT PRIMARY KEY, tenant_id BIGI` +
			`NT, user_id TEXT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE auth_tokens(id TEXT PRIMARY KEY, user_id TEXT, token` +
			`_type TEXT, is_revoked BOOLEAN, expires_at TIMESTAMPTZ)`,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY, tenant_id BIGINT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE messages(id TEXT PRIMARY KEY, session_id TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE message_artifacts(id TEXT PRIMARY KEY, session_id TE` +
			`XT, message_id TEXT, position INTEGER, url TEXT, file_name TEXT, ` +
			`file_type TEXT, file_size BIGINT, content_hash TEXT, source_path ` +
			`TEXT, mod_time TEXT, created_at TIMESTAMPTZ, deleted_at TIMESTAMP` +
			`TZ)`,
		// The migration only requires this function for a generation value.
		// Keep the stand-in in the rolled-back fixture schema, never public.
		`CREATE FUNCTION uuid_generate_v4() RETURNS uuid LANGUAGE sql AS $` +
			`$ SELECT '00000000-0000-4000-8000-000000000001'::uuid $$`,
		`INSERT INTO im_channels VALUES ('channel',7,'octo','{"account_id":"a","bot_uid":"b"}',NULL)`,
		`INSERT INTO im_channel_sessions VALUES ('mapping','session',7,'channel','octo',now())`,
		`INSERT INTO tenant_members VALUES (1,7,'alice','active',NULL),(2,7,'bob','active',NULL)`,
		`INSERT INTO users VALUES ('alice',true,NULL),('bob',true,NULL)`,
		`INSERT INTO auth_tokens VALUES ('token','alice','access_token',fa` +
			`lse,now()+interval '1 hour'),('bob-token','bob','access_token',fa` +
			`lse,now()+interval '1 hour')`,
		`INSERT INTO sessions VALUES ('session',7,NULL),('shared-session',7,NULL)`,
		`INSERT INTO messages VALUES ('m1','session',NULL),('m2','session'` +
			`,NULL),('shared-message','shared-session',NULL)`,
		`INSERT INTO message_artifacts(id,session_id,message_id,position,u` +
			`rl,file_name,file_type,file_size,content_hash,source_path,mod_tim` +
			`e,created_at) VALUES ('a1','session','m1',0,'resource://shared','` +
			`one.txt','.txt',10,'','/same','',now()),('a2','session','m2',0,'r` +
			`esource://other','two.txt','.txt',10,'','/same','',now()),('a3','` +
			`shared-session','shared-message',0,'resource://shared','shared.tx` +
			`t','.txt',10,'','/else','',now())`,
	} {
		require.NoError(t, tx.Exec(statement).Error)
	}
	migration, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "migrations", "versioned", "000134_im_channel_web_owners.up.sql",
	))
	require.NoError(t, err)
	require.NoError(t, tx.Exec(string(migration)).Error)
	require.NoError(t, tx.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_`+
		`user_id) VALUES ('owner',7,'channel','alice')`).Error)

	repo := NewMessageRepository(tx)
	deleter := repo.(interface {
		DeleteChannelArtifact(
			context.Context, uint64, string, string, time.Time, *types.ArtifactDeleteRequest,
		) ([]types.MessageArtifactRecord, []types.ArtifactRef, error)
	})
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour)
	req := &types.ArtifactDeleteRequest{SessionID: "session", MessageID: "m2", Index: 0, AllVersions: true}
	// Unbound callers cannot delete even a known artifact.
	_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "bob", "bob-token", expiry, req)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	var live int64
	require.NoError(t, tx.Table("message_artifacts").
		Where("session_id = ? AND deleted_at IS NULL", "session").Count(&live).Error)
	require.EqualValues(t, 2, live)
	// The bound owner can delete both versions, including a cleared chat.
	rows, marked, err := deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", expiry, req)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.ElementsMatch(t, []types.ArtifactRef{
		{MessageID: "m1", Position: 0}, {MessageID: "m2", Position: 0},
	}, marked)
	require.NoError(t, tx.Table("message_artifacts").
		Where("session_id = ? AND deleted_at IS NULL", "session").Count(&live).Error)
	require.Zero(t, live)
	// The shared live artifact remains a guard against reclaiming its blob.
	live, err = repo.CountLiveArtifactsByURL(ctx, "resource://shared")
	require.NoError(t, err)
	require.EqualValues(t, 1, live)
	// Identity change uses the real migration trigger, revokes the old grant,
	// and retires historical mappings even after the same user is rebound.
	require.NoError(t, tx.Exec(`UPDATE message_artifacts SET deleted_at = NULL WHERE id = 'a2'`).Error)
	require.NoError(t, tx.Exec(`UPDATE im_channels SET credentials = '{"account_id":"new","bot_ui`+
		`d":"b"}' WHERE id = 'channel'`).Error)
	var retired int64
	require.NoError(t, tx.Table("im_channel_session_identity_retirements").
		Where("session_mapping_id = ?", "mapping").Count(&retired).Error)
	require.EqualValues(t, 1, retired)
	require.NoError(t, tx.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_`+
		`user_id) VALUES ('new-owner',7,'channel','alice')`).Error)
	_, _, err = deleter.DeleteChannelArtifact(ctx, 7, "alice", "token", expiry, req)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, tx.Table("message_artifacts").Where("id = ? AND deleted_at IS NULL", "a2").Count(&live).Error)
	require.EqualValues(t, 1, live)
}
