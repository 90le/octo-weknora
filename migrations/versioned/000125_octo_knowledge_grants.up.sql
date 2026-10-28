CREATE TABLE IF NOT EXISTS octo_scope_knowledge_grants (
 tenant_id BIGINT NOT NULL, scope_id VARCHAR(36) NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
 created_at TIMESTAMP NOT NULL, PRIMARY KEY (tenant_id, scope_id, knowledge_base_id)
);
ALTER TABLE octo_scopes ADD COLUMN IF NOT EXISTS allow_knowledge_creation BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE octo_scopes ADD COLUMN IF NOT EXISTS aggregate_child_issues BOOLEAN NOT NULL DEFAULT FALSE;
