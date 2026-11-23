CREATE TABLE octo_issue_remediations (
  issue_id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL,
  coordinator VARCHAR(128) NOT NULL, revision INTEGER NOT NULL,
  plan TEXT NOT NULL
);
CREATE INDEX idx_octo_remediations_coordinator ON octo_issue_remediations (tenant_id, coordinator);
ALTER TABLE octo_issue_retests ADD COLUMN scenario TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_issue_retests ADD COLUMN question TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_issue_retests ADD COLUMN plan_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_issue_retests ADD COLUMN document_id TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_issue_retests ADD COLUMN source_version TEXT NOT NULL DEFAULT '';
ALTER TABLE octo_issue_retests ADD COLUMN document_cited BOOLEAN NOT NULL DEFAULT FALSE;
