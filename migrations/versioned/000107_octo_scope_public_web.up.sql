-- Public web lookup must be enabled on each exact Octo group/subarea scope.
-- Existing scopes remain closed; inherit_parent applies to KB bindings only.
ALTER TABLE octo_scopes ADD COLUMN allow_public_web BOOLEAN NOT NULL DEFAULT FALSE;
