CREATE TABLE IF NOT EXISTS octo_message_inbox (
 channel_id VARCHAR(36) NOT NULL, message_id VARCHAR(255) NOT NULL,
 tenant_id BIGINT NOT NULL, authority TEXT NOT NULL DEFAULT '', input TEXT NOT NULL, reply TEXT NOT NULL DEFAULT '',
 state VARCHAR(32) NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(channel_id,message_id)
);
CREATE INDEX IF NOT EXISTS idx_octo_inbox_delivery ON octo_message_inbox(tenant_id,channel_id,state,updated_at);
