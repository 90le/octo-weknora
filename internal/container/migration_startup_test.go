package container

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitDatabaseRefusesFailedAutoMigration(t *testing.T) {
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "startup.db"))
	t.Setenv("AUTO_MIGRATE", "true")
	// The package test directory has no migrations/sqlite directory. This
	// exercises initDatabase's real migration failure path without a server.
	db, err := initDatabase(nil)
	require.Nil(t, db)
	require.ErrorContains(t, err, "database auto-migration failed; startup refused")
}

func TestInitDatabaseAllowsExternallyManagedSchema(t *testing.T) {
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "external.db"))
	t.Setenv("AUTO_MIGRATE", "false")
	db, err := initDatabase(nil)
	require.NoError(t, err)
	require.NotNil(t, db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}
