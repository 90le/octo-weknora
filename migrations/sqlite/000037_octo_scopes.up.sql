-- Octo usage scopes contain no credentials and grant no asset maintenance rights.
--
-- Renumbered from the fork's 000017 slot. This migration owns the whole Octo
-- scope column set so a Lite database created under the fork's 0.8.0 numbering
-- can replay the slot after the upstream 0.8.3 migrations; SQLite has no
-- ADD COLUMN IF NOT EXISTS, so the later slots only add tables and indexes.
CREATE TABLE IF NOT EXISTS octo_scopes (
 id VARCHAR(36) PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 group_id VARCHAR(128) NOT NULL,
 subarea_id VARCHAR(128) NOT NULL DEFAULT '',
 display_name VARCHAR(256) NOT NULL,
 name_source VARCHAR(32) NOT NULL DEFAULT 'configured',
 inherit_parent BOOLEAN NOT NULL DEFAULT FALSE,
 sync_status VARCHAR(32) NOT NULL DEFAULT 'unverified',
 sync_error VARCHAR(128) NOT NULL DEFAULT '',
 checked_at TIMESTAMP,
 verified_at TIMESTAMP,
 allow_knowledge_creation BOOLEAN NOT NULL DEFAULT 0,
 aggregate_child_issues BOOLEAN NOT NULL DEFAULT 0,
 allow_public_web BOOLEAN NOT NULL DEFAULT 0,
 created_at TIMESTAMP NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 UNIQUE (tenant_id, account_id, group_id, subarea_id),
 UNIQUE (tenant_id, id),
 CHECK (subarea_id <> '' OR inherit_parent = FALSE)
);
CREATE TABLE IF NOT EXISTS octo_scope_bindings (
 tenant_id BIGINT NOT NULL,
 scope_id VARCHAR(36) NOT NULL,
 knowledge_base_id VARCHAR(36) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id, scope_id, knowledge_base_id),
 FOREIGN KEY (tenant_id, scope_id) REFERENCES octo_scopes(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS octo_scope_bindings_kb ON octo_scope_bindings(tenant_id, knowledge_base_id);
