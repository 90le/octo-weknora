DROP TABLE IF EXISTS octo_scope_knowledge_grants;
ALTER TABLE octo_scopes DROP COLUMN allow_knowledge_creation;
ALTER TABLE octo_scopes DROP COLUMN aggregate_child_issues;
