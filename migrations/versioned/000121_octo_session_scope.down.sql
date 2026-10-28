-- Preflight before removing the new index: restoring legacy uniqueness must
-- fail without deleting user conversations if multiple Bots now share an Agent.
CREATE UNIQUE INDEX idx_channel_lookup_rollback_guard
 ON im_channel_sessions(platform,user_id,chat_id,tenant_id,agent_id)
 WHERE deleted_at IS NULL;
DROP INDEX idx_octo_channel_session;
DROP INDEX idx_channel_lookup;
ALTER INDEX idx_channel_lookup_rollback_guard RENAME TO idx_channel_lookup;
DROP INDEX idx_channel_thread_lookup;
CREATE UNIQUE INDEX idx_channel_thread_lookup
 ON im_channel_sessions(platform,chat_id,thread_id,tenant_id,agent_id)
 WHERE deleted_at IS NULL AND thread_id <> '';
