DROP TRIGGER IF EXISTS trg_guard_role_source_snapshot_retention ON role_source_snapshot;
DROP FUNCTION IF EXISTS guard_role_source_snapshot_retention();
DROP TRIGGER IF EXISTS trg_guard_role_source_task_pin_snapshot ON role_source_task_pin;
DROP FUNCTION IF EXISTS guard_role_source_task_pin_snapshot();
