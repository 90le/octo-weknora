package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// The DSN must point to a disposable PostgreSQL database, never production.
func TestPostgresLegacyEarlyProbeOnDisposablePostgres(t *testing.T) {
	dsn := os.Getenv("WEKNORA_MIGRATION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_MIGRATION_TEST_POSTGRES_DSN to an independent disposable PostgreSQL database")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("octoprobe_test_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		if err != nil {
			t.Errorf("remove disposable schema: %v", err)
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
	_, err = db.Exec("CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL); " +
		"INSERT INTO schema_migrations VALUES (96, false)")
	require.NoError(t, err)
	for version := 96; version <= 106; version++ {
		_, err = db.Exec("UPDATE schema_migrations SET version = $1", version)
		require.NoError(t, err)
		require.NoError(t, checkPostgresLegacyEarlyBeforeMigration(context.Background(), fixtureDSN, uint(version)))
	}
	_, err = db.Exec("CREATE TABLE octo_scopes (id text PRIMARY KEY); INSERT INTO octo_scopes VALUES ('preserve')")
	require.NoError(t, err)
	for version := 96; version <= 106; version++ {
		_, err = db.Exec("UPDATE schema_migrations SET version = $1", version)
		require.NoError(t, err)
		err = checkPostgresLegacyEarlyBeforeMigration(context.Background(), fixtureDSN, uint(version))
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
	for _, fixture := range []struct{ create, drop string }{
		{"CREATE TABLE local_source_spaces (id text)", "DROP TABLE local_source_spaces"},
		{"CREATE TABLE github_document_sync_items (id text)", "DROP TABLE github_document_sync_items"},
		{"CREATE TABLE data_sources (restart_recovery_lease_id text)", "DROP TABLE data_sources"},
		{
			"CREATE TABLE index_fixture (id text); CREATE INDEX idx_octo_channel_session ON index_fixture(id)",
			"DROP TABLE index_fixture",
		},
	} {
		_, err = db.Exec(fixture.create)
		require.NoError(t, err)
		err = checkPostgresLegacyEarlyBeforeMigration(context.Background(), fixtureDSN, 106)
		require.ErrorContains(t, err, "migration 106 has a fork Octo schema fingerprint")
		_, err = db.Exec(fixture.drop)
		require.NoError(t, err)
	}
}
