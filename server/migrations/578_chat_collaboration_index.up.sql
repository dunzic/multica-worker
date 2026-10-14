CREATE INDEX CONCURRENTLY idx_message_topic_created
    ON chat_message(topic_id, created_at)
    WHERE topic_id IS NOT NULL;
