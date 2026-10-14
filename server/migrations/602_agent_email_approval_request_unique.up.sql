CREATE UNIQUE INDEX CONCURRENTLY agent_email_approval_request_uidx ON agent_email_approval (workspace_id, agent_id, policy_version_id, request_key_digest) WHERE decision = 'requested';
