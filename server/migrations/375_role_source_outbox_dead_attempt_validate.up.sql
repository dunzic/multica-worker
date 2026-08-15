ALTER TABLE role_source_outbox
    VALIDATE CONSTRAINT role_source_outbox_replay_count_check,
    VALIDATE CONSTRAINT role_source_outbox_dead_attempt_check;
