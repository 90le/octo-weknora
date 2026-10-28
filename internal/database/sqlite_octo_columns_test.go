package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyOctoScopeSchema is the fork's 0.8.0 octo_scopes definition: it lacks
// every column that later slots add.
const legacyOctoScopeSchema = `
CREATE TABLE octo_scopes (
 id VARCHAR(36) PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 group_id VARCHAR(128) NOT NULL,
 subarea_id VARCHAR(128) NOT NULL DEFAULT '',
 display_name VARCHAR(256) NOT NULL,
 name_source VARCHAR(32) NOT NULL DEFAULT 'configured',
 inherit_parent BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMP NOT NULL,
 updated_at TIMESTAMP NOT NULL
);
CREATE TABLE octo_connections (
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 token TEXT NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id, account_id)
);
CREATE TABLE data_sources (id VARCHAR(36) PRIMARY KEY);
`

func TestReconcileSQLiteOctoColumnsAddsMissingColumns(t *testing.T) {
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "legacy.db"))
	_, err := db.Exec(legacyOctoScopeSchema)
	require.NoError(t, err)

	_, err = db.Exec(
		"INSERT INTO octo_scopes (id, tenant_id, account_id, group_id, display_name, created_at, updated_at) " +
			"VALUES ('scope-1', 1, 'acct', 'group', 'Group', '2026-01-01 00:00:00', '2026-01-01 00:00:00')",
	)
	require.NoError(t, err)

	require.NoError(t, ReconcileSQLiteOctoColumns(db))

	for _, column := range sqliteOctoColumns {
		require.Truef(t, hasSQLiteColumn(t, db, column.Table, column.Column),
			"reconciliation must add %s.%s", column.Table, column.Column)
	}

	var displayName, syncStatus string
	var allowPublicWeb bool
	require.NoError(t, db.QueryRow(
		"SELECT display_name, sync_status, allow_public_web FROM octo_scopes WHERE id = 'scope-1'",
	).Scan(&displayName, &syncStatus, &allowPublicWeb))
	require.Equal(t, "Group", displayName)
	require.Equal(t, "unverified", syncStatus)
	require.False(t, allowPublicWeb)

	// A second startup must be a no-op rather than a duplicate-column error.
	require.NoError(t, ReconcileSQLiteOctoColumns(db))
}

func TestReconcileSQLiteOctoColumnsSkipsAbsentTables(t *testing.T) {
	db := openSQLiteDB(t, filepath.Join(t.TempDir(), "empty.db"))
	require.NoError(t, ReconcileSQLiteOctoColumns(db))
	require.False(t, hasSQLiteColumn(t, db, "octo_scopes", "sync_status"))
}

func hasSQLiteColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(&count))
	return count == 1
}
