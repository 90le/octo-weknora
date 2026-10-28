package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
)

// sqliteOctoColumn is one column that must exist on an Octo or data-source
// table regardless of how old the SQLite file is.
type sqliteOctoColumn struct {
	Table      string
	Column     string
	Definition string
}

// sqliteOctoColumns matches the definitions in migrations/sqlite 000037
// (octo_scopes), 000038 (octo_connections) and the data-source lease columns
// of 000046. SQLite has no ADD COLUMN IF NOT EXISTS, and a Lite database
// created under the fork's 0.8.0 numbering (versions 17-28) already contains
// these objects when the renumbered slots replay, so the columns are
// reconciled by introspection instead of an unconditional ALTER.
var sqliteOctoColumns = []sqliteOctoColumn{
	{"octo_scopes", "sync_status", "VARCHAR(32) NOT NULL DEFAULT 'unverified'"},
	{"octo_scopes", "sync_error", "VARCHAR(128) NOT NULL DEFAULT ''"},
	{"octo_scopes", "checked_at", "TIMESTAMP"},
	{"octo_scopes", "verified_at", "TIMESTAMP"},
	{"octo_scopes", "allow_knowledge_creation", "BOOLEAN NOT NULL DEFAULT 0"},
	{"octo_scopes", "aggregate_child_issues", "BOOLEAN NOT NULL DEFAULT 0"},
	{"octo_scopes", "allow_public_web", "BOOLEAN NOT NULL DEFAULT 0"},
	{"octo_connections", "verified_identity", "TEXT NOT NULL DEFAULT '{}'"},
	{"data_sources", "restart_recovery_lease_id", "VARCHAR(36) NOT NULL DEFAULT ''"},
	{"data_sources", "restart_recovery_lease_until", "DATETIME NULL"},
}

// ReconcileSQLiteOctoColumns adds columns that a replayed Octo migration slot
// would otherwise try to add twice. It is safe to call on every start: tables
// and columns are looked up first, and nothing is dropped or rewritten.
func ReconcileSQLiteOctoColumns(db *sql.DB) error {
	if db == nil {
		return nil
	}
	ctx := context.Background()

	tables := make(map[string]bool, 3)
	for _, column := range sqliteOctoColumns {
		exists, ok := tables[column.Table]
		if !ok {
			var err error
			exists, err = sqliteObjectExists(ctx, db, "table", column.Table)
			if err != nil {
				return err
			}
			tables[column.Table] = exists
		}
		if !exists {
			continue
		}

		has, err := sqliteHasColumn(ctx, db, column.Table, column.Column)
		if err != nil {
			return err
		}
		if has {
			continue
		}

		statement := fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN %s %s",
			column.Table, column.Column, column.Definition,
		)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add SQLite column %s.%s: %w", column.Table, column.Column, err)
		}
		logger.Infof(ctx, "sqlite: added missing column %s.%s", column.Table, column.Column)
	}

	// The partial lease index depends on a column this function owns.
	if tables["data_sources"] {
		if _, err := db.ExecContext(ctx,
			"CREATE INDEX IF NOT EXISTS idx_data_sources_restart_recovery_lease "+
				"ON data_sources (restart_recovery_lease_until)"); err != nil {
			return fmt.Errorf("create SQLite restart-recovery lease index: %w", err)
		}
	}

	return nil
}

func sqliteObjectExists(ctx context.Context, db *sql.DB, kind, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", kind, name,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect SQLite %s %s: %w", kind, name, err)
	}
	return count > 0, nil
}

func sqliteHasColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("inspect SQLite column %s.%s: %w", table, column, err)
	}
	return count > 0, nil
}
