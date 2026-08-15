CREATE INDEX CONCURRENTLY role_source_secret_transfer_plan_idx ON role_source_secret_transfer (workspace_id, source_id, plan_digest, approval_id, role_id, created_at DESC, id DESC);
