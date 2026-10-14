CREATE INDEX CONCURRENTLY email_message_expired_sending_idx ON email_message (lease_expires_at, id) WHERE status = 'sending';
