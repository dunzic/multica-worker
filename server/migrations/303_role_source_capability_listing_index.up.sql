CREATE INDEX CONCURRENTLY role_source_capability_listing_idx ON role_source_capability_version (workspace_id, capability_id, version, created_at DESC);
