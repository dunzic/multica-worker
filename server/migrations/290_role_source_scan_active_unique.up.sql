CREATE UNIQUE INDEX CONCURRENTLY role_source_scan_active_unique ON role_source_scan_request (source_id) WHERE status IN ('queued', 'claimed');
