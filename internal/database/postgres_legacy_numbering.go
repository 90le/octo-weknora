package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
)

const postgres107Advice = "take an isolated, consistent backup and review the schema and migration history in a disposable restore; use a reviewed, controlled recovery procedure before restarting; do not force or rewind the version automatically"

func isPostgresMigrationDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

func postgresDirtyError(version uint) error {
	return fmt.Errorf("PostgreSQL migration %d is dirty; its state and history are unsafe to infer: %s", version, postgres107Advice)
}

func isPostgresLegacyEarlyVersion(version uint) bool {
	return version >= 96 && version <= 106
}

// These objects and columns were introduced by the fork's 96-106 migrations.
// Even a partial fingerprint is enough to make the shared version ambiguous.
// Resolve objects through search_path, as the migration connection does.
const postgresLegacyEarlyFingerprintSQL = `SELECT
    EXISTS (SELECT 1 FROM unnest(ARRAY[
        'octo_scopes', 'octo_scope_bindings', 'octo_connections', 'octo_scope_bindings_kb',
        'idx_octo_channel_session', 'local_source_spaces', 'local_source_roots',
        'local_source_registry_state', 'idx_local_source_roots_tenant',
        'octo_knowledge_issues', 'octo_knowledge_issue_events', 'octo_knowledge_contacts',
        'octo_knowledge_proposals', 'idx_octo_issues_scope', 'idx_octo_issues_kb',
        'idx_octo_issue_events', 'idx_octo_contacts_kb', 'idx_octo_contacts_default',
        'idx_octo_proposals_owner', 'octo_message_inbox', 'idx_octo_inbox_delivery',
        'octo_scope_knowledge_grants', 'octo_report_schedules', 'octo_report_runs',
        'idx_octo_reports_due', 'idx_octo_reports_tenant', 'idx_octo_report_runs_schedule',
        'datasource_restart_recovery_runs', 'idx_data_sources_restart_recovery_lease',
        'idx_datasource_restart_recovery_runs_source_status', 'idx_datasource_restart_recovery_runs_resume',
        'github_document_sync_runs', 'github_document_sync_items', 'idx_github_document_one_running_run',
        'idx_github_document_runs_scope', 'idx_github_document_runs_lease', 'idx_github_document_items_pending'
    ]) AS names(name) WHERE to_regclass(name) IS NOT NULL)
    OR EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('data_sources')
        AND attname IN ('restart_recovery_lease_id', 'restart_recovery_lease_until')
        AND attnum > 0 AND NOT attisdropped)`

func checkPostgresLegacyEarlyBeforeMigration(ctx context.Context, dsn string, version uint) error {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("cannot inspect PostgreSQL migration %d connection configuration; %s", version, postgres107Advice)
	}
	db, err := sql.Open("postgres", migrate.FilterCustomQuery(parsed).String())
	if err != nil {
		return fmt.Errorf("cannot open PostgreSQL migration %d schema inspection; %s", version, postgres107Advice)
	}
	defer db.Close()
	return checkPostgresLegacyEarlyFingerprint(ctx, db, version)
}

func checkPostgresLegacyEarlyFingerprint(ctx context.Context, db *sql.DB, version uint) error {
	var octo bool
	if err := db.QueryRowContext(ctx, postgresLegacyEarlyFingerprintSQL).Scan(&octo); err != nil {
		return fmt.Errorf("cannot inspect PostgreSQL migration %d schema; %s", version, postgres107Advice)
	}
	if octo {
		return fmt.Errorf("PostgreSQL migration %d has a fork Octo schema fingerprint and overlaps upstream numbering; %s", version, postgres107Advice)
	}
	return nil
}

// postgres107FingerprintSQL uses the connection's search_path, like the
// migrator. A partial Octo or upstream fingerprint is ambiguous, not proof of
// a particular history. These are markers, not a proof that every migration or
// data-only change ran. Also check the earlier fork schema and the concurrent
// message index so an incomplete upstream history is not silently accepted.
const postgres107FingerprintSQL = `SELECT
    to_regclass('octo_scopes') IS NOT NULL,
    to_regclass('octo_scope_bindings') IS NOT NULL,
    to_regclass('octo_connections') IS NOT NULL,
    to_regclass('mcp_endpoints') IS NOT NULL,
    to_regclass('message_artifacts') IS NOT NULL,
    EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('message_artifacts') AND attname = 'deleted_at' AND attnum > 0 AND NOT attisdropped),
    EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('messages') AND attname = 'context_checkpoint' AND attnum > 0 AND NOT attisdropped),
    to_regclass('fork_snapshot_leases') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('sessions') AND attname = 'fork_bootstrap' AND attnum > 0 AND NOT attisdropped),
    EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = to_regclass('tenant_skills') AND attname = 'served' AND attnum > 0 AND NOT attisdropped) AND EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('idx_messages_session_created_id') AND indisvalid)`

func checkPostgres107BeforeMigration(ctx context.Context, dsn string, dirty bool) error {
	if dirty {
		return postgresDirtyError(107)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("cannot inspect PostgreSQL migration 107 connection configuration; %s", postgres107Advice)
	}
	// golang-migrate strips its x-* options before passing the DSN to lib/pq.
	db, err := sql.Open("postgres", migrate.FilterCustomQuery(parsed).String())
	if err != nil {
		return fmt.Errorf("cannot open PostgreSQL migration 107 schema inspection; %s", postgres107Advice)
	}
	defer db.Close()
	return checkPostgres107Fingerprint(ctx, db)
}

func checkPostgres107Fingerprint(ctx context.Context, db *sql.DB) error {
	var scopes, bindings, connections, mcp, artifacts, artifactDeleted, checkpoint, forkSchema, skillAndIndex bool
	if err := db.QueryRowContext(ctx, postgres107FingerprintSQL).Scan(
		&scopes, &bindings, &connections, &mcp, &artifacts, &artifactDeleted, &checkpoint, &forkSchema, &skillAndIndex,
	); err != nil {
		return fmt.Errorf("cannot inspect PostgreSQL migration 107 schema; %s", postgres107Advice)
	}
	octo := scopes || bindings || connections
	upstreamComplete := mcp && artifacts && artifactDeleted && checkpoint && forkSchema && skillAndIndex
	if octo && !upstreamComplete {
		return fmt.Errorf("PostgreSQL migration 107 has an Octo fingerprint and missing upstream 96-107 objects, consistent with the fork's legacy numbering; %s", postgres107Advice)
	}
	if octo || !upstreamComplete {
		return fmt.Errorf("PostgreSQL migration 107 has an ambiguous or incomplete schema; neither fork nor upstream numbering can be safely assumed; %s", postgres107Advice)
	}
	return nil
}
