ALTER TABLE octo_issue_remediations ADD COLUMN verification_mode VARCHAR(16) NOT NULL DEFAULT 'strict';
ALTER TABLE octo_issue_remediations ADD COLUMN updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP;

ALTER TABLE octo_report_schedules ADD COLUMN sender_channel_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE octo_report_schedules ADD COLUMN delivery_targets_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE octo_report_schedules ADD COLUMN scope_filter_json TEXT NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS octo_report_run_deliveries (
  id VARCHAR(64) PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  run_id VARCHAR(64) NOT NULL,
  schedule_id VARCHAR(64) NOT NULL DEFAULT '',
  sender_channel VARCHAR(128) NOT NULL,
  target_key VARCHAR(256) NOT NULL,
  target_kind VARCHAR(16) NOT NULL,
  target_label VARCHAR(256) NOT NULL DEFAULT '',
  status VARCHAR(24) NOT NULL,
  receipt VARCHAR(256) NOT NULL DEFAULT '',
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  idempotency_key VARCHAR(128) NOT NULL UNIQUE,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  sent_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_octo_run_deliveries_run ON octo_report_run_deliveries (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS idx_octo_inbox_tenant_created ON octo_message_inbox (tenant_id, created_at DESC);
