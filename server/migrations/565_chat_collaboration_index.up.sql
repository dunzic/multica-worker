CREATE INDEX CONCURRENTLY idx_conversation_ws_updated
    ON conversation(workspace_id, updated_at DESC);
