package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
)

// The fork shipped its Octo migrations as SQLite 17-28 on top of upstream
// v0.8.0, whose own sequence stopped at 16. Upstream v0.8.3 then numbered its
// new migrations 17-36, so a Lite database from the fork would skip them: the
// migrator only runs versions above the recorded one, and it does not know the
// recorded 28 came from a different sequence.
const (
	forkLiteSharedTailVersion = 16
	forkLiteLegacyMinVersion  = 17
	forkLiteLegacyMaxVersion  = 28
)

// rewindForkLiteNumbering moves a Lite database created under the fork's 0.8.0
// numbering back to the last version both sequences share, so the upstream
// 17-36 migrations and the renumbered Octo slots at 37-48 both run in order.
//
// The guard is the Octo schema itself: no database produced by the upstream
// sequence has an octo_scopes table before version 37, so a database at 17-28
// that owns one was numbered by the fork. Nothing is dropped or rewritten here;
// the replay relies on the Octo slots being written for it and on
// ReconcileSQLiteOctoColumns filling in columns a replayed slot cannot add.
func rewindForkLiteNumbering(ctx context.Context, db *sql.DB, version int) error {
	if db == nil || version < forkLiteLegacyMinVersion || version > forkLiteLegacyMaxVersion {
		return nil
	}
	exists, err := sqliteObjectExists(ctx, db, "table", "octo_scopes")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE schema_migrations SET version = ?, dirty = 0", forkLiteSharedTailVersion); err != nil {
		return fmt.Errorf("rewind fork Lite migration numbering: %w", err)
	}
	logger.Warnf(ctx,
		"sqlite: Lite database recorded fork 0.8.0 version %d; rewound to %d so migrations 17-48 replay",
		version, forkLiteSharedTailVersion)
	return nil
}
