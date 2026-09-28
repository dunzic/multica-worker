CREATE INDEX CONCURRENTLY agent_email_approval_latest_idx ON agent_email_approval (workspace_id, agent_id, policy_version_id, approval_authority_digest, created_at DESC, id DESC);
