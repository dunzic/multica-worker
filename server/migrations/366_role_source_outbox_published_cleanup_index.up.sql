CREATE INDEX CONCURRENTLY role_source_outbox_published_cleanup_idx ON role_source_outbox (published_at, id) WHERE status = 'published';
