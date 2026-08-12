CREATE UNIQUE INDEX CONCURRENTLY role_source_artifact_digest_unique ON role_source_artifact (workspace_id, digest);
