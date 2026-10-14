CREATE INDEX CONCURRENTLY agent_email_quota_agent_window_idx ON agent_email_quota_reservation (workspace_id, agent_id, created_at);
