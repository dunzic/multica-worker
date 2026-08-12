DROP TRIGGER IF EXISTS trg_capture_role_source_task_pin ON agent_task_queue;
DROP FUNCTION IF EXISTS capture_role_source_task_pin();
DROP FUNCTION IF EXISTS role_source_agent_state_digest(UUID, UUID, TEXT);
DROP TRIGGER IF EXISTS trg_reject_role_source_task_pin_update ON role_source_task_pin;
DROP FUNCTION IF EXISTS reject_role_source_task_pin_update();
DROP TRIGGER IF EXISTS trg_invalidate_stale_role_source_tasks ON role_source_object_mapping;
DROP FUNCTION IF EXISTS invalidate_stale_role_source_tasks();
