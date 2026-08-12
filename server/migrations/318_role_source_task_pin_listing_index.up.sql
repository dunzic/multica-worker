CREATE INDEX CONCURRENTLY role_source_task_pin_listing_idx ON role_source_task_pin (workspace_id, source_id, created_at DESC, task_id);
