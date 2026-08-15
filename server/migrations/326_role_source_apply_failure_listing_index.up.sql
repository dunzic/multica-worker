CREATE INDEX CONCURRENTLY role_source_apply_failure_listing_idx ON role_source_apply_failure (workspace_id, source_id, occurred_at DESC, id DESC);
