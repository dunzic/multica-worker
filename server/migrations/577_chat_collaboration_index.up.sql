CREATE INDEX CONCURRENTLY idx_topic_conv_last_msg
    ON topic(conversation_id, last_message_at DESC);
