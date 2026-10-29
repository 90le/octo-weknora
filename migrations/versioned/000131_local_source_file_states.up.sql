CREATE TABLE IF NOT EXISTS local_source_file_states (
  tenant_id BIGINT NOT NULL,
  data_source_id VARCHAR(36) NOT NULL,
  selection VARCHAR(64) NOT NULL,
  path_hash VARCHAR(64) NOT NULL,
  path TEXT NOT NULL,
  status VARCHAR(32) NOT NULL,
  reason VARCHAR(64) NOT NULL DEFAULT '',
  source_size BIGINT NOT NULL DEFAULT 0,
  source_version VARCHAR(64) NOT NULL DEFAULT '',
  synced_version VARCHAR(64) NOT NULL DEFAULT '',
  has_previous BOOLEAN NOT NULL DEFAULT FALSE,
  observed_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (tenant_id, data_source_id, selection, path_hash)
);
ALTER TABLE local_source_file_states ADD COLUMN IF NOT EXISTS synced_version VARCHAR(64) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_local_source_file_states_status
  ON local_source_file_states (tenant_id, data_source_id, selection, status);
