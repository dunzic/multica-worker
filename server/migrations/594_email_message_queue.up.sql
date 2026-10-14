CREATE INDEX CONCURRENTLY email_message_queue_idx ON email_message (next_attempt_at, created_at, id) WHERE status = 'queued';
