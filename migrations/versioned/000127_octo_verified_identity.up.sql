ALTER TABLE octo_connections ADD COLUMN IF NOT EXISTS verified_identity JSONB NOT NULL DEFAULT '{}';
