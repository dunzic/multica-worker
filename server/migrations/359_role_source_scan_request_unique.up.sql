CREATE UNIQUE INDEX CONCURRENTLY role_source_scan_request_unique ON role_source_scan_request (source_id, request_key_digest) WHERE request_key_digest IS NOT NULL;
