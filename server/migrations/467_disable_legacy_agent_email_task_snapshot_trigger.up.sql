-- Plugin V2 removed plugin_execution_manifest in migration 344. The Agent
-- email snapshot trigger still referenced that relation and consequently
-- blocked every task insert. Disable the incompatible legacy trigger. A task
-- without a pinned email authorization snapshot cannot send production email,
-- so this restores task creation while preserving fail-closed authorization.
DROP TRIGGER IF EXISTS trg_agent_email_task_authorization_snapshot ON agent_task_queue;
