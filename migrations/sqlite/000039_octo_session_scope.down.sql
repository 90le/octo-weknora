CREATE UNIQUE INDEX idx_channel_lookup_rollback_guard
 ON im_channel_sessions(platform,user_id,chat_id,tenant_id)
 WHERE deleted_at IS NULL;
DROP INDEX idx_octo_channel_session;
DROP INDEX idx_channel_lookup;
CREATE UNIQUE INDEX idx_channel_lookup
 ON im_channel_sessions(platform,user_id,chat_id,tenant_id)
 WHERE deleted_at IS NULL;
DROP INDEX idx_channel_lookup_rollback_guard;
DROP INDEX idx_channel_thread_lookup;
CREATE UNIQUE INDEX idx_channel_thread_lookup
 ON im_channel_sessions(platform,chat_id,thread_id,tenant_id)
 WHERE deleted_at IS NULL AND thread_id <> '';
