ALTER TABLE octo_scopes ADD COLUMN member_questions_enabled BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE octo_scopes SET member_questions_enabled = TRUE;
