CREATE TABLE octo_issue_retests (
  id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, issue_id VARCHAR(36) NOT NULL,
  actor_uid VARCHAR(128) NOT NULL, question_message_id VARCHAR(32) NOT NULL DEFAULT '',
  state VARCHAR(20) NOT NULL CHECK (state IN ('running','completed','failed','interrupted')),
  answer TEXT NOT NULL DEFAULT '', model_id VARCHAR(128) NOT NULL DEFAULT '', sources TEXT NOT NULL DEFAULT '[]',
  error_code VARCHAR(32) NOT NULL DEFAULT '', review_state VARCHAR(20) NOT NULL DEFAULT '',
  review_note TEXT NOT NULL DEFAULT '', reviewer_uid VARCHAR(128) NOT NULL DEFAULT '', reviewed_at TIMESTAMP,
  finished_at TIMESTAMP, created_at TIMESTAMP NOT NULL,
  CHECK (review_state IN ('','verified','unresolved','insufficient'))
);
CREATE UNIQUE INDEX idx_octo_issue_retests_running ON octo_issue_retests (tenant_id,issue_id) WHERE state = 'running';
CREATE INDEX idx_octo_issue_retests_history ON octo_issue_retests (tenant_id,issue_id,created_at);
CREATE INDEX idx_octo_issue_retests_period ON octo_issue_retests (tenant_id,finished_at,issue_id);
