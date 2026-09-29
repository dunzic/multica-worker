CREATE UNIQUE INDEX CONCURRENTLY email_message_idempotency_uidx ON email_message (workspace_id, agent_id, idempotency_key_digest);
