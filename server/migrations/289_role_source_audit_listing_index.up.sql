CREATE INDEX CONCURRENTLY role_source_audit_listing_idx ON role_source_audit_event (workspace_id, source_id, sequence DESC);
