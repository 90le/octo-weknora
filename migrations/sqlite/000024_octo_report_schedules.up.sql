CREATE TABLE IF NOT EXISTS octo_report_schedules (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, name VARCHAR(256) NOT NULL,
 channel_id VARCHAR(128) NOT NULL, scope_id VARCHAR(128) NOT NULL,
 recipient_type VARCHAR(16) NOT NULL CHECK (recipient_type IN ('source','private')),
 recipient_uid VARCHAR(128) NOT NULL DEFAULT '',
 weekday INTEGER NOT NULL, hour INTEGER NOT NULL, minute INTEGER NOT NULL,
 timezone VARCHAR(128) NOT NULL, format VARCHAR(16) NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 created_by VARCHAR(128) NOT NULL, next_run_at TIMESTAMP NOT NULL,
 last_run_at TIMESTAMP, last_status VARCHAR(32) NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_reports_due ON octo_report_schedules (enabled,next_run_at);
CREATE INDEX IF NOT EXISTS idx_octo_reports_tenant ON octo_report_schedules (tenant_id,id);
CREATE TABLE IF NOT EXISTS octo_report_runs (
 id VARCHAR(64) PRIMARY KEY, tenant_id BIGINT NOT NULL, schedule_id VARCHAR(36) NOT NULL,
 due_at TIMESTAMP NOT NULL, status VARCHAR(32) NOT NULL,
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_report_runs_schedule ON octo_report_runs (tenant_id,schedule_id,due_at);

