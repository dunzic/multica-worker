CREATE INDEX CONCURRENTLY role_source_apply_listing_idx ON role_source_apply (workspace_id, source_id, created_at DESC, id);
