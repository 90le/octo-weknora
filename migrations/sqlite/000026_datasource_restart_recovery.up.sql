-- SQLite counterpart of PostgreSQL migration 000105. SQLite stores JSON as
-- TEXT; the application owns validation and never lets recovery plans contain
-- source bytes, credentials, or host paths.
ALTER TABLE data_sources ADD COLUMN restart_recovery_lease_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE data_sources ADD COLUMN restart_recovery_lease_until DATETIME NULL;
CREATE INDEX IF NOT EXISTS idx_data_sources_restart_recovery_lease
    ON data_sources (restart_recovery_lease_until);

CREATE TABLE IF NOT EXISTS datasource_restart_recovery_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    data_source_id VARCHAR(36) NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(36) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    plan_digest VARCHAR(64) NOT NULL,
    plan TEXT NOT NULL DEFAULT '{}',
    status VARCHAR(32) NOT NULL,
    lease_id VARCHAR(36) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME NULL,
    finished_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_datasource_restart_recovery_runs_source_status
    ON datasource_restart_recovery_runs (data_source_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_datasource_restart_recovery_runs_resume
    ON datasource_restart_recovery_runs (tenant_id, status, updated_at);
