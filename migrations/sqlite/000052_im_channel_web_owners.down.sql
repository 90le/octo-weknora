DROP TRIGGER IF EXISTS trg_im_channel_web_owner_revoke_update;
DROP TRIGGER IF EXISTS trg_im_channel_web_owner_revoke_delete;
DROP TRIGGER IF EXISTS trg_im_channel_identity_generation_insert;
-- Do not reconnect retired sessions on rollback.
DROP TABLE IF EXISTS im_channel_session_identity_retirements;
ALTER TABLE im_channels DROP COLUMN owner_identity_generation;
DROP TABLE IF EXISTS im_channel_web_owners;
