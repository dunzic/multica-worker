CREATE INDEX CONCURRENTLY role_source_approval_listing_idx ON role_source_plan_approval (workspace_id, source_id, plan_digest, created_at DESC, id);
