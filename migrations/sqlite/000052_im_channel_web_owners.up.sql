CREATE TABLE im_channel_web_owners (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    im_channel_id VARCHAR(36) NOT NULL,
    web_user_id VARCHAR(36) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE UNIQUE INDEX idx_im_channel_web_owners_active ON im_channel_web_owners (tenant_id, im_channel_id, web_user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_im_channel_web_owners_user ON im_channel_web_owners (tenant_id, web_user_id) WHERE deleted_at IS NULL;
ALTER TABLE im_channels ADD COLUMN owner_identity_generation TEXT NOT NULL DEFAULT '';

-- Preserve the original association for audit while making retired sessions
-- ineligible for any future binding of the reused channel ID.
CREATE TABLE im_channel_session_identity_retirements (
    session_mapping_id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    im_channel_id VARCHAR(36) NOT NULL,
    retired_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER trg_im_channel_identity_generation_insert
AFTER INSERT ON im_channels
BEGIN
    UPDATE im_channels SET owner_identity_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;

CREATE TRIGGER trg_im_channel_web_owner_revoke_update
AFTER UPDATE OF platform, tenant_id, id, deleted_at, credentials ON im_channels
WHEN OLD.platform = 'octo' AND (
    OLD.platform IS NOT NEW.platform OR OLD.tenant_id IS NOT NEW.tenant_id OR OLD.id IS NOT NEW.id OR OLD.deleted_at IS NOT NEW.deleted_at OR
    CASE
      WHEN json_valid(OLD.credentials) = 0 OR json_valid(NEW.credentials) = 0 THEN 1
      WHEN json_type(OLD.credentials) IS NOT 'object' OR json_type(NEW.credentials) IS NOT 'object' THEN 1
      WHEN (SELECT count(*) FROM json_each(OLD.credentials) WHERE key='account_id') != 1 OR
           (SELECT count(*) FROM json_each(OLD.credentials) WHERE key='bot_uid') != 1 OR
           (SELECT count(*) FROM json_each(NEW.credentials) WHERE key='account_id') != 1 OR
           (SELECT count(*) FROM json_each(NEW.credentials) WHERE key='bot_uid') != 1 THEN 1
      WHEN json_type(OLD.credentials,'$.account_id') IS NOT 'text' OR json_type(OLD.credentials,'$.bot_uid') IS NOT 'text' OR
           json_type(NEW.credentials,'$.account_id') IS NOT 'text' OR json_type(NEW.credentials,'$.bot_uid') IS NOT 'text' THEN 1
      WHEN trim(json_extract(OLD.credentials,'$.account_id'), char(32,9,13,10)) = '' OR
           trim(json_extract(OLD.credentials,'$.bot_uid'), char(32,9,13,10)) = '' OR
           trim(json_extract(NEW.credentials,'$.account_id'), char(32,9,13,10)) = '' OR
           trim(json_extract(NEW.credentials,'$.bot_uid'), char(32,9,13,10)) = '' THEN 1
      ELSE json_extract(OLD.credentials,'$.account_id') IS NOT json_extract(NEW.credentials,'$.account_id') OR
           json_extract(OLD.credentials,'$.bot_uid') IS NOT json_extract(NEW.credentials,'$.bot_uid')
    END)
BEGIN
    UPDATE im_channels SET owner_identity_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
    INSERT OR IGNORE INTO im_channel_session_identity_retirements(session_mapping_id, tenant_id, im_channel_id)
    SELECT id, tenant_id, im_channel_id FROM im_channel_sessions
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo';
    UPDATE im_channel_sessions SET im_channel_id = '', deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP)
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo';
    INSERT INTO audit_logs (tenant_id, action, target_type, target_id, target_user_id, outcome)
    SELECT tenant_id, 'im_channel.web_owner_revoked', 'im_channel', im_channel_id, web_user_id, 'success'
    FROM im_channel_web_owners WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
    UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
END;
CREATE TRIGGER trg_im_channel_web_owner_revoke_delete
AFTER DELETE ON im_channels WHEN OLD.platform = 'octo'
BEGIN
    INSERT OR IGNORE INTO im_channel_session_identity_retirements(session_mapping_id, tenant_id, im_channel_id)
    SELECT id, tenant_id, im_channel_id FROM im_channel_sessions
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo';
    UPDATE im_channel_sessions SET im_channel_id = '', deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP)
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo';
    INSERT INTO audit_logs (tenant_id, action, target_type, target_id, target_user_id, outcome)
    SELECT tenant_id, 'im_channel.web_owner_revoked', 'im_channel', im_channel_id, web_user_id, 'success'
    FROM im_channel_web_owners WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
    UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP
    WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
END;
