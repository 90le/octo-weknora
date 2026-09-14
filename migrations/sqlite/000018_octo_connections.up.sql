CREATE TABLE octo_connections (
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 token TEXT NOT NULL,
 updated_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id, account_id)
);
ALTER TABLE octo_scopes ADD COLUMN sync_status VARCHAR(32) NOT NULL DEFAULT 'unverified';
ALTER TABLE octo_scopes ADD COLUMN sync_error VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE octo_scopes ADD COLUMN checked_at TIMESTAMP;
ALTER TABLE octo_scopes ADD COLUMN verified_at TIMESTAMP;
