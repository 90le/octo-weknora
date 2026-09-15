DROP INDEX IF EXISTS idx_channel_lookup;
CREATE UNIQUE INDEX idx_channel_lookup
 ON im_channel_sessions(platform,user_id,chat_id,tenant_id)
 WHERE deleted_at IS NULL AND platform <> 'octo';
DROP INDEX IF EXISTS idx_channel_thread_lookup;
CREATE UNIQUE INDEX idx_channel_thread_lookup
 ON im_channel_sessions(platform,chat_id,thread_id,tenant_id)
 WHERE deleted_at IS NULL AND thread_id <> '' AND platform <> 'octo';
CREATE UNIQUE INDEX idx_octo_channel_session
 ON im_channel_sessions(platform,user_id,chat_id,tenant_id,agent_id,im_channel_id)
 WHERE deleted_at IS NULL AND platform = 'octo';
