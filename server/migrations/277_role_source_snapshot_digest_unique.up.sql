CREATE UNIQUE INDEX CONCURRENTLY role_source_snapshot_digest_unique ON role_source_snapshot (source_id, snapshot_digest);
