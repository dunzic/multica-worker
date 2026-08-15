CREATE INDEX CONCURRENTLY role_source_outbox_dead_cleanup_idx ON role_source_outbox (created_at, id) WHERE status = 'dead';
