CREATE INDEX CONCURRENTLY role_source_artifact_delete_intent_due_idx ON role_source_artifact_delete_intent (next_attempt_at, storage_key) WHERE state IN ('pending', 'deleting', 'tombstoned');
