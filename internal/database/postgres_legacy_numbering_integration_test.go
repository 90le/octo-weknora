package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This creates and removes only a unique schema in the explicitly supplied
// disposable PostgreSQL database. Never set the DSN to a production database.
func TestPostgres107LegacyMigrationRefusesWithoutRewind(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to an independent disposable PostgreSQL database")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("octo107_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		if err != nil {
			t.Errorf("remove disposable test schema: %v", err)
		}
		_ = admin.Close()
	})
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	fixtureDSN := parsed.String()
	db, err := sql.Open("postgres", fixtureDSN)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);
		INSERT INTO schema_migrations VALUES (107, false);
		CREATE TABLE octo_scopes (id text PRIMARY KEY)`)
	require.NoError(t, err)
	chdirAndRestore(t, sqliteRepoRoot(t))
	for _, dirty := range []bool{false, true} {
		_, err = db.Exec("UPDATE schema_migrations SET dirty = $1", dirty)
		require.NoError(t, err)
		err = RunMigrationsWithOptions(fixtureDSN, MigrationOptions{AutoRecoverDirty: true, StopOnMigrationError: true})
		if dirty {
			require.ErrorContains(t, err, "107 is dirty")
		} else {
			require.ErrorContains(t, err, "fork's legacy numbering")
		}
		var version int
		var gotDirty bool
		require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &gotDirty))
		require.Equal(t, 107, version)
		require.Equal(t, dirty, gotDirty)
	}
	_, err = db.Exec("UPDATE schema_migrations SET version = 108, dirty = true")
	require.NoError(t, err)
	err = RunMigrationsWithOptions(fixtureDSN, MigrationOptions{AutoRecoverDirty: true})
	require.ErrorContains(t, err, "108 is dirty")
	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 108, version)
	require.True(t, dirty)
	_, err = db.Exec(`DROP TABLE octo_scopes;
		CREATE TABLE mcp_endpoints (id text);
		CREATE TABLE message_artifacts (deleted_at timestamptz);
		CREATE TABLE messages (id text, context_checkpoint jsonb);
		CREATE INDEX idx_messages_session_created_id ON messages(id);
		CREATE TABLE sessions (fork_bootstrap jsonb);
		CREATE TABLE fork_snapshot_leases (id text);
		CREATE TABLE tenant_skills (served jsonb)`)
	require.NoError(t, err)
	require.NoError(t, checkPostgres107Fingerprint(context.Background(), db))
}

func TestPostgresLegacyEarlyRealMigrationBoundaries(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to an independent disposable PostgreSQL database")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("octoearly_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		if err != nil {
			t.Errorf("remove disposable test schema: %v", err)
		}
		_ = admin.Close()
	})
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	fixtureDSN := parsed.String()
	db, err := sql.Open("postgres", fixtureDSN)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);
		INSERT INTO schema_migrations VALUES (96, false);
		CREATE TABLE octo_scopes (id text PRIMARY KEY);
		INSERT INTO octo_scopes VALUES ('preserve')`)
	require.NoError(t, err)
	chdirAndRestore(t, sqliteRepoRoot(t))

	// The scope table is introduced in the fork's 96 and persists through 106.
	// Each attempt uses its own recorded version, without rewinding to 95 or 107.
	for version := 96; version <= 106; version++ {
		_, err = db.Exec("UPDATE schema_migrations SET version = $1, dirty = false", version)
		require.NoError(t, err)
		err = RunMigrationsWithOptions(fixtureDSN, MigrationOptions{AutoRecoverDirty: true, StopOnMigrationError: true})
		require.ErrorContains(t, err, fmt.Sprintf("migration %d has a fork Octo schema fingerprint", version))
		var gotVersion, rows int
		var dirty bool
		require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&gotVersion, &dirty))
		require.Equal(t, version, gotVersion)
		require.False(t, dirty)
		require.NoError(t, db.QueryRow("SELECT count(*) FROM octo_scopes WHERE id = 'preserve'").Scan(&rows))
		require.Equal(t, 1, rows)
	}
	_, err = db.Exec("DROP TABLE octo_scopes")
	require.NoError(t, err)
	for version := uint(96); version <= 106; version++ {
		require.NoError(t, checkPostgresLegacyEarlyFingerprint(context.Background(), db, version),
			"normal upstream marker check at %d", version)
	}
	for _, fixture := range []struct{ create, drop string }{
		{"CREATE TABLE local_source_spaces (id text)", "DROP TABLE local_source_spaces"},
		{"CREATE TABLE github_document_sync_items (id text)", "DROP TABLE github_document_sync_items"},
		{"CREATE TABLE data_sources (restart_recovery_lease_id text)", "DROP TABLE data_sources"},
		{"CREATE TABLE index_fixture (id text); CREATE INDEX idx_octo_channel_session ON index_fixture(id)", "DROP TABLE index_fixture"},
	} {
		_, err = db.Exec(fixture.create)
		require.NoError(t, err)
		err = RunMigrationsWithOptions(fixtureDSN, MigrationOptions{StopOnMigrationError: true})
		require.ErrorContains(t, err, "migration 106 has a fork Octo schema fingerprint")
		_, err = db.Exec(fixture.drop)
		require.NoError(t, err)
	}
	// A representative upstream-106 fixture reaches the 107 Up migration. The
	// deliberately incomplete fixture then fails later, proving the guard did
	// not reject it without pretending this is a full upstream upgrade test.
	_, err = db.Exec(`CREATE TABLE message_artifacts (session_id text, created_at timestamptz, url text)`)
	require.NoError(t, err)
	err = RunMigrationsWithOptions(fixtureDSN, MigrationOptions{StopOnMigrationError: true})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "fork Octo schema fingerprint")
	var gotVersion int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&gotVersion, &dirty))
	require.GreaterOrEqual(t, gotVersion, 107)
}
