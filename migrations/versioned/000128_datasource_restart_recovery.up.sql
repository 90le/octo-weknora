-- Durable, local-file-only replay plans for document candidates interrupted by
-- a Lite-process restart. The lease sits on the data source so scheduled,
-- manual, and queued sync work can observe the same fence without a second
-- coordinator.
ALTER TABLE data_sources
    ADD COLUMN IF NOT EXISTS restart_recovery_lease_id VARCHAR(36),
    ADD COLUMN IF NOT EXISTS restart_recovery_lease_until TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_data_sources_restart_recovery_lease
    ON data_sources (restart_recovery_lease_until)
    WHERE restart_recovery_lease_until IS NOT NULL;

CREATE TABLE IF NOT EXISTS datasource_restart_recovery_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    data_source_id VARCHAR(36) NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(36) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    plan_digest VARCHAR(64) NOT NULL,
    plan JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(32) NOT NULL,
    lease_id VARCHAR(36) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMP NULL,
    finished_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_datasource_restart_recovery_runs_source_status
    ON datasource_restart_recovery_runs (data_source_id, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_datasource_restart_recovery_runs_resume
    ON datasource_restart_recovery_runs (tenant_id, status, updated_at)
    WHERE status IN ('pending', 'running');
