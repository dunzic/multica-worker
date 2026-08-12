CREATE INDEX CONCURRENTLY role_source_active_task_impact_idx ON agent_task_queue (created_at DESC, id DESC) WHERE status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory');
