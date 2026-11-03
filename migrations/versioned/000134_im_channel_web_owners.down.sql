DROP TRIGGER IF EXISTS trg_im_channel_web_owner_revoke ON im_channels;
DROP TRIGGER IF EXISTS trg_im_channel_identity_generation_insert ON im_channels;
DROP FUNCTION IF EXISTS initialize_im_channel_identity_generation();
DROP FUNCTION IF EXISTS revoke_im_channel_web_owners();
-- Do not reconnect retired sessions on rollback.
DROP TABLE IF EXISTS im_channel_session_identity_retirements;
ALTER TABLE im_channels DROP COLUMN owner_identity_generation;
DROP TABLE IF EXISTS im_channel_web_owners;
