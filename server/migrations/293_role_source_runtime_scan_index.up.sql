CREATE INDEX CONCURRENTLY role_source_runtime_scan_idx ON role_source (runtime_id, id) WHERE state IN ('registered', 'active', 'error');
