CREATE INDEX CONCURRENTLY role_source_plan_listing_idx ON role_source_plan (workspace_id, source_id, created_at DESC, plan_digest);
