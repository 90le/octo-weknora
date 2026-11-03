CREATE TABLE im_channel_web_owners (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    im_channel_id VARCHAR(36) NOT NULL,
    web_user_id VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_im_channel_web_owners_active ON im_channel_web_owners (tenant_id, im_channel_id, web_user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_im_channel_web_owners_user ON im_channel_web_owners (tenant_id, web_user_id) WHERE deleted_at IS NULL;
ALTER TABLE im_channels ADD COLUMN owner_identity_generation VARCHAR(36) NOT NULL DEFAULT '';

CREATE TABLE im_channel_session_identity_retirements (
    session_mapping_id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    im_channel_id VARCHAR(36) NOT NULL,
    retired_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE FUNCTION initialize_im_channel_identity_generation() RETURNS trigger AS $$
BEGIN
    UPDATE im_channels SET owner_identity_generation = uuid_generate_v4()::text WHERE id = NEW.id;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_im_channel_identity_generation_insert
AFTER INSERT ON im_channels FOR EACH ROW EXECUTE FUNCTION initialize_im_channel_identity_generation();

-- PostgreSQL jsonb rejects malformed JSON on write. Only identity fields and
-- channel lifecycle changes affect existing grants.
CREATE FUNCTION revoke_im_channel_web_owners() RETURNS trigger AS $$
BEGIN
    IF OLD.platform = 'octo' AND (TG_OP = 'DELETE' OR
       jsonb_typeof(OLD.credentials) IS DISTINCT FROM 'object' OR
       jsonb_typeof(NEW.credentials) IS DISTINCT FROM 'object' OR
       jsonb_typeof(OLD.credentials->'account_id') IS DISTINCT FROM 'string' OR
       jsonb_typeof(OLD.credentials->'bot_uid') IS DISTINCT FROM 'string' OR
       jsonb_typeof(NEW.credentials->'account_id') IS DISTINCT FROM 'string' OR
       jsonb_typeof(NEW.credentials->'bot_uid') IS DISTINCT FROM 'string' OR
       btrim(OLD.credentials->>'account_id', E' \t\r\n') = '' OR
       btrim(OLD.credentials->>'bot_uid', E' \t\r\n') = '' OR
       btrim(NEW.credentials->>'account_id', E' \t\r\n') = '' OR
       btrim(NEW.credentials->>'bot_uid', E' \t\r\n') = '' OR
       OLD.credentials->>'account_id' IS DISTINCT FROM NEW.credentials->>'account_id' OR
       OLD.credentials->>'bot_uid' IS DISTINCT FROM NEW.credentials->>'bot_uid' OR
       OLD.platform IS DISTINCT FROM NEW.platform OR
       OLD.tenant_id IS DISTINCT FROM NEW.tenant_id OR
       OLD.id IS DISTINCT FROM NEW.id OR
       OLD.deleted_at IS DISTINCT FROM NEW.deleted_at) THEN
        IF TG_OP = 'UPDATE' THEN
            UPDATE im_channels SET owner_identity_generation = uuid_generate_v4()::text WHERE id = NEW.id;
        END IF;
        INSERT INTO im_channel_session_identity_retirements(session_mapping_id, tenant_id, im_channel_id)
        SELECT id, tenant_id, im_channel_id FROM im_channel_sessions
        WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo'
        ON CONFLICT (session_mapping_id) DO NOTHING;
        UPDATE im_channel_sessions SET im_channel_id = '', deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP)
        WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND platform = 'octo';
        INSERT INTO audit_logs (tenant_id, action, target_type, target_id, target_user_id, outcome)
        SELECT tenant_id, 'im_channel.web_owner_revoked', 'im_channel', im_channel_id, web_user_id, 'success'
        FROM im_channel_web_owners
        WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
        UPDATE im_channel_web_owners SET deleted_at = CURRENT_TIMESTAMP
        WHERE tenant_id = OLD.tenant_id AND im_channel_id = OLD.id AND deleted_at IS NULL;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_im_channel_web_owner_revoke
AFTER UPDATE OF platform, tenant_id, id, deleted_at, credentials OR DELETE ON im_channels
FOR EACH ROW EXECUTE FUNCTION revoke_im_channel_web_owners();
