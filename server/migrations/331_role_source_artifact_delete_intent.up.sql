-- Durable object-deletion intent survives workspace teardown and storage/API
-- crashes. It intentionally has no workspace_id: workspace-scoped teardown
-- must keep this cleanup obligation until its tombstone tail completes.
CREATE TABLE role_source_artifact_delete_intent (
    storage_key TEXT NOT NULL CHECK (char_length(storage_key) BETWEEN 1 AND 1024),
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
    reason TEXT NOT NULL CHECK (reason IN ('unreachable', 'workspace_deleted')),
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'deleting', 'tombstoned')),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    tombstone_pass INTEGER NOT NULL DEFAULT 0 CHECK (tombstone_pass >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((state = 'deleting' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL) OR state <> 'deleting')
);
