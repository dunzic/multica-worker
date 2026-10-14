CREATE UNIQUE INDEX CONCURRENTLY agent_email_quota_message_uidx ON agent_email_quota_reservation (workspace_id, message_id);
