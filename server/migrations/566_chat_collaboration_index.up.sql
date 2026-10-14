CREATE UNIQUE INDEX CONCURRENTLY idx_conversation_dm_unique
    ON conversation(workspace_id, dm_signature)
    WHERE dm_signature IS NOT NULL AND archived_at IS NULL;
