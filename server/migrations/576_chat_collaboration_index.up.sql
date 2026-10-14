CREATE INDEX CONCURRENTLY idx_participant_active_lookup
    ON conversation_participant(principal_type, principal_id, conversation_id)
    WHERE left_at IS NULL;
