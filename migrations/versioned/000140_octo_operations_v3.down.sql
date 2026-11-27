DROP INDEX IF EXISTS idx_octo_inbox_tenant_created;
DROP INDEX IF EXISTS idx_octo_run_deliveries_run;
DROP TABLE IF EXISTS octo_report_run_deliveries;

ALTER TABLE octo_report_schedules DROP COLUMN IF EXISTS scope_filter_json;
ALTER TABLE octo_report_schedules DROP COLUMN IF EXISTS delivery_targets_json;
ALTER TABLE octo_report_schedules DROP COLUMN IF EXISTS sender_channel_id;

ALTER TABLE octo_issue_remediations DROP COLUMN IF EXISTS updated_at;
ALTER TABLE octo_issue_remediations DROP COLUMN IF EXISTS verification_mode;
