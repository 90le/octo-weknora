CREATE TABLE local_source_spaces (
 id VARCHAR(128) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 path TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL
);
CREATE TABLE local_source_roots (
 id VARCHAR(128) PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 space_id VARCHAR(128) NOT NULL REFERENCES local_source_spaces(id),
 name VARCHAR(128) NOT NULL,
 directory TEXT NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMP NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 UNIQUE (tenant_id, space_id, directory)
);
CREATE INDEX idx_local_source_roots_tenant ON local_source_roots(tenant_id);
CREATE TABLE local_source_registry_state (
 id VARCHAR(128) PRIMARY KEY,
 imported_at TIMESTAMP NOT NULL
);
