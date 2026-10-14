CREATE INDEX CONCURRENTLY idx_message_sender_v2
    ON chat_message(sender_type, sender_id)
    WHERE sender_type IS NOT NULL;
