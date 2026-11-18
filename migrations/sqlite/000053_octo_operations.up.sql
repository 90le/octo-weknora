ALTER TABLE octo_knowledge_issues ADD COLUMN archive_state VARCHAR(16) NOT NULL DEFAULT ''
 CHECK (archive_state IN ('','archived','trash'));
CREATE INDEX idx_octo_issues_archive ON octo_knowledge_issues (tenant_id,archive_state,created_at);
CREATE INDEX idx_octo_inbox_quality ON octo_message_inbox (tenant_id,channel_id,created_at,message_id);

CREATE TABLE octo_quality_reviews (
 tenant_id BIGINT NOT NULL, channel_id VARCHAR(256) NOT NULL, scope_id VARCHAR(128) NOT NULL,
 message_id VARCHAR(256) NOT NULL, library_id VARCHAR(36) NOT NULL DEFAULT '',
 state VARCHAR(20) NOT NULL DEFAULT '', suggestion TEXT NOT NULL DEFAULT '',
 model_id VARCHAR(36) NOT NULL DEFAULT '', finding TEXT NOT NULL DEFAULT '', evidence_signature VARCHAR(64) NOT NULL DEFAULT '',
 attempt_count INTEGER NOT NULL DEFAULT 0, available_at TIMESTAMP,
 disposition VARCHAR(20) NOT NULL DEFAULT '', topic VARCHAR(160) NOT NULL DEFAULT '',
 group_id VARCHAR(128) NOT NULL DEFAULT '', issue_id VARCHAR(36) NOT NULL DEFAULT '',
 updated_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id,channel_id,scope_id,message_id)
);
CREATE INDEX idx_octo_quality_review_queue ON octo_quality_reviews (state,available_at,tenant_id);

CREATE TABLE octo_quality_settings (
 tenant_id BIGINT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 model_id VARCHAR(36) NOT NULL DEFAULT '', per_run INTEGER NOT NULL DEFAULT 4,
 daily_limit INTEGER NOT NULL DEFAULT 24, sample_percent INTEGER NOT NULL DEFAULT 10,
 updated_at TIMESTAMP NOT NULL
);

CREATE TABLE octo_quality_attempts (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, channel_id VARCHAR(256) NOT NULL,
 scope_id VARCHAR(128) NOT NULL, message_id VARCHAR(256) NOT NULL, started_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_octo_quality_attempt_budget ON octo_quality_attempts (tenant_id,started_at);

CREATE TABLE octo_issue_replies (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, issue_id VARCHAR(36) NOT NULL,
 actor_uid VARCHAR(128) NOT NULL, channel_id VARCHAR(256) NOT NULL, scope_name TEXT NOT NULL,
 target VARCHAR(20) NOT NULL, user_name TEXT NOT NULL, user_uid VARCHAR(128) NOT NULL,
 message_id VARCHAR(32) NOT NULL, body TEXT NOT NULL, signature VARCHAR(64) NOT NULL,
 status VARCHAR(20) NOT NULL, receipt VARCHAR(32) NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_octo_issue_reply_history ON octo_issue_replies (tenant_id,issue_id,created_at);

ALTER TABLE octo_report_schedules ADD COLUMN template VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE octo_report_schedules ADD COLUMN frequency VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE octo_report_schedules ADD COLUMN period VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE octo_report_schedules ADD COLUMN mode VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE octo_report_schedules ADD COLUMN lookback INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN interval_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN month_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN threshold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN minimum_users INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN only_changes BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE octo_report_schedules ADD COLUMN instruction TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_report_runs ADD COLUMN from_at TIMESTAMP;
ALTER TABLE octo_report_runs ADD COLUMN to_at TIMESTAMP;
ALTER TABLE octo_report_runs ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_report_runs ADD COLUMN snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_report_runs ADD COLUMN fingerprint VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE octo_report_runs ADD COLUMN authority VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE octo_report_runs ADD COLUMN revision TIMESTAMP;
ALTER TABLE octo_report_runs ADD COLUMN error_code VARCHAR(64) NOT NULL DEFAULT '';
