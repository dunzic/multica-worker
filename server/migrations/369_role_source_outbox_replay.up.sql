ALTER TABLE role_source_outbox
    ADD COLUMN replay_count SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN last_replayed_at TIMESTAMPTZ,
    ADD CONSTRAINT role_source_outbox_replay_count_check CHECK (replay_count BETWEEN 0 AND 3) NOT VALID,
    ADD CONSTRAINT role_source_outbox_dead_attempt_check CHECK (status <> 'dead' OR attempt = 20) NOT VALID;

CREATE TABLE role_source_outbox_replay (
    id UUID NOT NULL,
    outbox_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    apply_id UUID NOT NULL,
    authorization_id UUID NOT NULL,
    generation SMALLINT NOT NULL CHECK (generation BETWEEN 1 AND 3),
    reason_code TEXT NOT NULL CHECK (reason_code IN ('dependency_recovered', 'incident_recovery', 'delivery_reconciliation')),
    incident_reference_digest TEXT NOT NULL CHECK (incident_reference_digest ~ '^sha256:[0-9a-f]{64}$'),
    requester_key_id TEXT NOT NULL CHECK (requester_key_id ~ '^[a-z][a-z0-9_-]{1,63}$'),
    approver_key_id TEXT NOT NULL CHECK (approver_key_id ~ '^[a-z][a-z0-9_-]{1,63}$'),
    authorization_digest TEXT NOT NULL CHECK (authorization_digest ~ '^sha256:[0-9a-f]{64}$'),
    requester_signature_digest TEXT NOT NULL CHECK (requester_signature_digest ~ '^sha256:[0-9a-f]{64}$'),
    approver_signature_digest TEXT NOT NULL CHECK (approver_signature_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_receipt_digest TEXT NOT NULL CHECK (expected_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    previous_replay_digest TEXT CHECK (previous_replay_digest IS NULL OR previous_replay_digest ~ '^sha256:[0-9a-f]{64}$'),
    replay_digest TEXT NOT NULL CHECK (replay_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (requester_key_id <> approver_key_id),
    CHECK ((generation = 1) = (previous_replay_digest IS NULL))
);
