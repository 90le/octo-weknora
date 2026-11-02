CREATE TABLE IF NOT EXISTS octo_card_bindings (
  tenant_id BIGINT NOT NULL,
  channel_id VARCHAR(36) NOT NULL,
  proposal_id VARCHAR(64) NOT NULL,
  message_id VARCHAR(32) NOT NULL DEFAULT '',
  client_msg_no VARCHAR(64) NOT NULL,
  state VARCHAR(24) NOT NULL DEFAULT 'pending',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (channel_id, proposal_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_octo_card_binding_message
  ON octo_card_bindings (tenant_id, channel_id, message_id) WHERE message_id <> '';

CREATE TABLE IF NOT EXISTS octo_card_events (
  tenant_id BIGINT NOT NULL,
  channel_id VARCHAR(36) NOT NULL,
  event_key VARCHAR(64) NOT NULL,
  event_id BIGINT NOT NULL,
  message_id VARCHAR(32) NOT NULL,
  target_id VARCHAR(256) NOT NULL,
  target_type INTEGER NOT NULL,
  operator_uid VARCHAR(128) NOT NULL,
  action_id VARCHAR(64) NOT NULL,
  state VARCHAR(24) NOT NULL DEFAULT 'waiting',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (channel_id, event_key)
);
CREATE INDEX IF NOT EXISTS idx_octo_card_events_waiting
  ON octo_card_events (tenant_id, channel_id, state, created_at);

CREATE TABLE IF NOT EXISTS octo_card_event_cursors (
  tenant_id BIGINT NOT NULL,
  channel_id VARCHAR(36) PRIMARY KEY,
  event_id BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
