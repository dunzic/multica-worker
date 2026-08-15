CREATE INDEX CONCURRENTLY role_source_outbox_due_idx ON role_source_outbox (next_attempt_at, created_at, id) WHERE status IN ('pending', 'publishing');
