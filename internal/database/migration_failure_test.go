package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationFailureStopsWithoutAutoRetry(t *testing.T) {
	root := t.TempDir()
	migrations := filepath.Join(root, "migrations", "sqlite")
	require.NoError(t, os.MkdirAll(migrations, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(migrations, "000001_broken.up.sql"),
		[]byte("INSERT INTO missing_table VALUES (1);"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(migrations, "000001_broken.down.sql"), []byte("SELECT 1;"), 0o600))
	chdirAndRestore(t, root)
	path := filepath.Join(root, "db.sqlite")
	err := RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{
		SQLiteDBPath: path, AutoRecoverDirty: true, StopOnMigrationError: true,
	})
	require.ErrorContains(t, err, "automatic retry disabled")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 1, version)
	require.True(t, dirty)
}
