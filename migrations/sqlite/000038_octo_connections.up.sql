-- Renumbered from the fork's 000018 slot. verified_identity is part of the
-- CREATE so the slot is replayable; the Octo scope columns it used to ALTER
-- are created by 000037.
CREATE TABLE IF NOT EXISTS octo_connections (
 tenant_id BIGINT NOT NULL,
 account_id VARCHAR(128) NOT NULL,
 token TEXT NOT NULL,
 verified_identity TEXT NOT NULL DEFAULT '{}',
 updated_at TIMESTAMP NOT NULL,
 PRIMARY KEY (tenant_id, account_id)
);
