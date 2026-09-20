CREATE TABLE IF NOT EXISTS octo_knowledge_issues (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL, channel_id VARCHAR(256) NOT NULL,
 scope_id VARCHAR(128) NOT NULL DEFAULT '', scope_name VARCHAR(256) NOT NULL DEFAULT '',
 group_id VARCHAR(128) NOT NULL DEFAULT '', subarea_id VARCHAR(128) NOT NULL DEFAULT '',
 is_direct BOOLEAN NOT NULL DEFAULT FALSE, knowledge_base_id VARCHAR(36) NOT NULL,
 kind VARCHAR(32) NOT NULL CHECK (kind IN ('missing','bug','suggestion')),
 title TEXT NOT NULL, description TEXT NOT NULL, expected TEXT NOT NULL DEFAULT '', steps TEXT NOT NULL DEFAULT '',
 reporter_uid VARCHAR(128) NOT NULL, reporter_name VARCHAR(256) NOT NULL DEFAULT '',
 message_id VARCHAR(256) NOT NULL, original_message TEXT NOT NULL, attachments JSON NOT NULL DEFAULT '[]',
 owner_uid VARCHAR(128) NOT NULL DEFAULT '', owner_name VARCHAR(256) NOT NULL DEFAULT '',
 status VARCHAR(32) NOT NULL DEFAULT 'open' CHECK (status IN ('open','inprogress','resolved','closed')),
 idempotency_key VARCHAR(64) NOT NULL UNIQUE, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_issues_scope ON octo_knowledge_issues (tenant_id,account_id,channel_id,scope_id,created_at);
CREATE INDEX IF NOT EXISTS idx_octo_issues_kb ON octo_knowledge_issues (tenant_id,knowledge_base_id,status);
CREATE TABLE IF NOT EXISTS octo_knowledge_issue_events (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, issue_id VARCHAR(36) NOT NULL,
 actor_uid VARCHAR(128) NOT NULL, actor_name VARCHAR(256) NOT NULL DEFAULT '',
 status VARCHAR(32) NOT NULL, note TEXT NOT NULL DEFAULT '', idempotency_key VARCHAR(64) NOT NULL UNIQUE, created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_issue_events ON octo_knowledge_issue_events (tenant_id,issue_id,created_at);
CREATE TABLE IF NOT EXISTS octo_knowledge_contacts (
 id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
 topic VARCHAR(256) NOT NULL DEFAULT '', name VARCHAR(256) NOT NULL, uid VARCHAR(128) NOT NULL,
 details TEXT NOT NULL DEFAULT '', is_default BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_contacts_kb ON octo_knowledge_contacts (tenant_id,knowledge_base_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_octo_contacts_default ON octo_knowledge_contacts (tenant_id,knowledge_base_id) WHERE is_default = TRUE;
CREATE TABLE IF NOT EXISTS octo_knowledge_proposals (
 id VARCHAR(64) PRIMARY KEY, tenant_id BIGINT NOT NULL, account_id VARCHAR(128) NOT NULL,
 channel_id VARCHAR(256) NOT NULL, scope_id VARCHAR(128) NOT NULL DEFAULT '', user_id VARCHAR(128) NOT NULL,
 action VARCHAR(64) NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '', payload JSON NOT NULL,
 status VARCHAR(32) NOT NULL DEFAULT 'pending', result JSON, source_message_id VARCHAR(256) NOT NULL,
 idempotency_key VARCHAR(64) NOT NULL UNIQUE, expires_at TIMESTAMP NOT NULL,
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_octo_proposals_owner ON octo_knowledge_proposals (tenant_id,channel_id,user_id,status);
