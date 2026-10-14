CREATE UNIQUE INDEX CONCURRENTLY idx_topic_legacy_chat_session ON topic(legacy_chat_session_id) WHERE legacy_chat_session_id IS NOT NULL;
