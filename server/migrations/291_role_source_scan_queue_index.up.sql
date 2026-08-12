CREATE INDEX CONCURRENTLY role_source_scan_queue_idx ON role_source_scan_request (status, requested_at, id) WHERE status IN ('queued', 'claimed');
