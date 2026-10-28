ALTER TABLE octo_scopes DROP COLUMN verified_at;
ALTER TABLE octo_scopes DROP COLUMN checked_at;
ALTER TABLE octo_scopes DROP COLUMN sync_error;
ALTER TABLE octo_scopes DROP COLUMN sync_status;
DROP TABLE octo_connections;
