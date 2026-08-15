DROP TABLE IF EXISTS role_source_outbox_replay;
ALTER TABLE role_source_outbox DROP CONSTRAINT IF EXISTS role_source_outbox_dead_attempt_check;
ALTER TABLE role_source_outbox DROP COLUMN IF EXISTS last_replayed_at;
ALTER TABLE role_source_outbox DROP COLUMN IF EXISTS replay_count;
