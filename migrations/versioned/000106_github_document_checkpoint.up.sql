-- GitHub document progress is separate from data_sources.last_sync_cursor.
-- The latter remains the last fully acknowledged manifest for old binaries.
CREATE TABLE github_document_sync_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    data_source_id VARCHAR(36) NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    selection VARCHAR(64) NOT NULL,
    commit_sha VARCHAR(40) NOT NULL,
    plan_digest VARCHAR(64) NOT NULL,
    credential_scope VARCHAR(64) NOT NULL,
    target_cursor JSONB NOT NULL,
    force_full BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(24) NOT NULL,
    lease_id VARCHAR(36) NOT NULL DEFAULT '',
    lease_until TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_github_document_one_running_run
    ON github_document_sync_runs(data_source_id) WHERE status = 'running';
CREATE INDEX idx_github_document_runs_scope
    ON github_document_sync_runs(tenant_id, knowledge_base_id, data_source_id, updated_at);
CREATE INDEX idx_github_document_runs_lease
    ON github_document_sync_runs(lease_until) WHERE lease_until IS NOT NULL;

CREATE TABLE github_document_sync_items (
    run_id VARCHAR(36) NOT NULL REFERENCES github_document_sync_runs(id) ON DELETE CASCADE,
    path_hash VARCHAR(64) NOT NULL,
    path TEXT NOT NULL,
    blob_sha VARCHAR(40) NOT NULL DEFAULT '',
    operation VARCHAR(8) NOT NULL,
    status VARCHAR(16) NOT NULL,
    outcome VARCHAR(16) NOT NULL DEFAULT '',
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(run_id, path_hash)
);
CREATE INDEX idx_github_document_items_pending
    ON github_document_sync_items(run_id, status, operation);
