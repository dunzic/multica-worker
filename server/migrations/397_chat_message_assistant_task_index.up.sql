-- Authorized delivery retries reconstruct the exact persisted assistant output
-- by task. Keep this lookup bounded on large chat histories.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_message_assistant_task
    ON chat_message (task_id, created_at DESC)
    WHERE role = 'assistant';
