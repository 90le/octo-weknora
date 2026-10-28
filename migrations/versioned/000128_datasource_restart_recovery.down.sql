DROP TABLE IF EXISTS datasource_restart_recovery_runs;

DROP INDEX IF EXISTS idx_data_sources_restart_recovery_lease;
ALTER TABLE data_sources
    DROP COLUMN IF EXISTS restart_recovery_lease_until,
    DROP COLUMN IF EXISTS restart_recovery_lease_id;
