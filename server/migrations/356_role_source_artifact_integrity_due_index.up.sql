CREATE INDEX CONCURRENTLY role_source_artifact_integrity_due_idx ON role_source_artifact_integrity (next_check_at, workspace_id, artifact_digest) WHERE state IN ('pending', 'checking', 'healthy');
