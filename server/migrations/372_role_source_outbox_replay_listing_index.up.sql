CREATE INDEX CONCURRENTLY role_source_outbox_replay_listing_idx ON role_source_outbox_replay (workspace_id, created_at DESC, id DESC);
