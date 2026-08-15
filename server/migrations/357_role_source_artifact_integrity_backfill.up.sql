INSERT INTO role_source_artifact_integrity (
    workspace_id, artifact_digest, storage_key, size_bytes, state, next_check_at
)
SELECT workspace_id, digest, storage_key, size_bytes, 'pending', now()
FROM role_source_artifact
ON CONFLICT (workspace_id, artifact_digest) DO NOTHING;
