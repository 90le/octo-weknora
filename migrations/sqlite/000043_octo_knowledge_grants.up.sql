-- Renumbered from the fork's 000023 slot. The octo_scopes columns this slot
-- used to add (allow_knowledge_creation, aggregate_child_issues) are part of
-- 000037 so replaying the slot stays safe on SQLite.
CREATE TABLE IF NOT EXISTS octo_scope_knowledge_grants (
 tenant_id BIGINT NOT NULL, scope_id VARCHAR(36) NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
 created_at TIMESTAMP NOT NULL, PRIMARY KEY (tenant_id, scope_id, knowledge_base_id)
);
