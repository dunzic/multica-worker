CREATE INDEX CONCURRENTLY role_source_snapshot_listing_idx ON role_source_snapshot (workspace_id, source_id, created_at DESC, snapshot_digest);
