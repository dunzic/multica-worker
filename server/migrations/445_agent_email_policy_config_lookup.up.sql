CREATE INDEX CONCURRENTLY agent_email_policy_config_idx ON agent_email_policy_version (workspace_id, agent_id, config_digest, created_at DESC, id DESC);
