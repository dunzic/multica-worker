CREATE INDEX CONCURRENTLY role_source_legal_hold_listing_idx ON role_source_legal_hold (workspace_id, source_id, created_at DESC, id);
