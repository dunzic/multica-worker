CREATE INDEX CONCURRENTLY email_message_expired_sending_v2_idx ON email_message (workspace_id, lease_expires_at, id) WHERE status = 'sending';
