ALTER TABLE octo_report_schedules ADD COLUMN quiet_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN quiet_end INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN cooldown_hours INTEGER NOT NULL DEFAULT 0;
ALTER TABLE octo_report_schedules ADD COLUMN topic TEXT NOT NULL DEFAULT '';
