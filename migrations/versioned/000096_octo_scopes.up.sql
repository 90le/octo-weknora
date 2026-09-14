-- Octo usage scopes contain no credentials and grant no asset maintenance rights.
CREATE TABLE octo_scopes (
 id VARCHAR(36) PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 group_id VARCHAR(128) NOT NULL,
 subarea_id VARCHAR(128) NOT NULL DEFAULT '',
 display_name VARCHAR(256) NOT NULL,
 name_source VARCHAR(32) NOT NULL DEFAULT 'configured',
 inherit_parent BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMP NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 UNIQUE (tenant_id, account_id, group_id, subarea_id),
 UNIQUE (tenant_id, id),
 CHECK (subarea_id <> '' OR inherit_parent = FALSE)
);
CREATE TABLE octo_scope_bindings (
 tenant_id BIGINT NOT NULL,
 scope_id VARCHAR(36) NOT NULL,
 knowledge_base_id VARCHAR(36) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id, scope_id, knowledge_base_id),
 FOREIGN KEY (tenant_id, scope_id) REFERENCES octo_scopes(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX octo_scope_bindings_kb ON octo_scope_bindings(tenant_id, knowledge_base_id);
