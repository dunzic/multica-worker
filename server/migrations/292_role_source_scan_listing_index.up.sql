CREATE INDEX CONCURRENTLY role_source_scan_listing_idx ON role_source_scan_request (workspace_id, source_id, requested_at DESC, id);
