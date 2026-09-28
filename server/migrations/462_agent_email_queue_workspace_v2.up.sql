CREATE INDEX CONCURRENTLY email_message_queue_v2_idx ON email_message (workspace_id, next_attempt_at, created_at, id) WHERE status = 'queued' AND attempt_count < 20;
