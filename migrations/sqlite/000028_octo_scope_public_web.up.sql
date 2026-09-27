-- Existing Octo scopes remain closed until explicitly authorized.
ALTER TABLE octo_scopes ADD COLUMN allow_public_web BOOLEAN NOT NULL DEFAULT 0;
