ALTER TABLE role_source_outbox
    DROP CONSTRAINT IF EXISTS role_source_outbox_replay_count_check,
    DROP CONSTRAINT IF EXISTS role_source_outbox_dead_attempt_check,
    ADD CONSTRAINT role_source_outbox_replay_count_check CHECK (replay_count BETWEEN 0 AND 3) NOT VALID,
    ADD CONSTRAINT role_source_outbox_dead_attempt_check CHECK (status <> 'dead' OR attempt = 20) NOT VALID;
