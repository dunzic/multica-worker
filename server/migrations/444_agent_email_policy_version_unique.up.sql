CREATE UNIQUE INDEX CONCURRENTLY agent_email_policy_version_uidx ON agent_email_policy_version (workspace_id, agent_id, version);
