-- Additive ledger; never store file bytes, signed URLs, or credentials.
CREATE TABLE IF NOT EXISTS octo_file_deliveries (
 tenant_id BIGINT NOT NULL, channel_id VARCHAR(36) NOT NULL,
 source_message_id VARCHAR(255) NOT NULL, file_key VARCHAR(64) NOT NULL,
 owner_id VARCHAR(36) NOT NULL DEFAULT '', state VARCHAR(16) NOT NULL,
 file_name TEXT NOT NULL DEFAULT '', content_hash VARCHAR(64) NOT NULL DEFAULT '',
 client_msg_no VARCHAR(36) NOT NULL DEFAULT '', platform_message_id VARCHAR(255) NOT NULL DEFAULT '',
 error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (tenant_id, channel_id, source_message_id, file_key)
);
CREATE INDEX IF NOT EXISTS idx_octo_file_delivery_recovery
 ON octo_file_deliveries(tenant_id, channel_id, source_message_id, state);
