package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Run only against the explicitly configured disposable migration-test DB.
func TestPostgresChannelIdentityRetirement(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to run PostgreSQL migration tests")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.Begin()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback()) })
	_, err = tx.Exec(`CREATE SCHEMA octo_identity_migration_fixture;
SET LOCAL search_path TO octo_identity_migration_fixture, public;
CREATE TABLE im_channels(id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT, platform TEXT,
    credentials JSONB, deleted_at TIMESTAMPTZ);
CREATE TABLE im_channel_sessions(id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT,
    im_channel_id VARCHAR(36), platform TEXT, deleted_at TIMESTAMPTZ);
CREATE TABLE audit_logs(id BIGSERIAL PRIMARY KEY, tenant_id BIGINT, action TEXT, target_type TEXT,
    target_id TEXT, target_user_id TEXT, outcome TEXT);
INSERT INTO im_channels VALUES ('c',7,'octo','{"account_id":"a","bot_uid":"b"}',NULL);
INSERT INTO im_channel_sessions VALUES ('old',7,'c','octo',NULL),('cleared',7,'c','octo',now());`)
	require.NoError(t, err)
	migration, err := os.ReadFile(
		filepath.Join(sqliteRepoRoot(t), "migrations", "versioned", "000134_im_channel_web_owners.up.sql"),
	)
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO im_channel_web_owners(id,tenant_id,im_channel_id,web_user_id)
VALUES ('o',7,'c','u');
UPDATE im_channels SET credentials='{"account_id":"new","bot_uid":"b"}' WHERE id='c'`)
	require.NoError(t, err)
	var retired, bound int
	var generation string
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM im_channel_session_identity_retirements`).Scan(&retired))
	require.Equal(t, 2, retired)
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM im_channel_web_owners WHERE deleted_at IS NULL`).Scan(&bound))
	require.Zero(t, bound)
	require.NoError(t, tx.QueryRow(`SELECT owner_identity_generation FROM im_channels WHERE id='c'`).Scan(&generation))
	require.NotEmpty(t, generation)
	_, err = tx.Exec(`UPDATE im_channels SET credentials='{}' WHERE id='c'`)
	require.NoError(t, err) // Invalid identity must not recurse through the generation update.
}
