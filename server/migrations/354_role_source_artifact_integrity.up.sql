-- Mutable, content-free integrity state is kept separate from the immutable
-- artifact readiness ledger. A quarantined row makes its matching readiness
-- row unusable until the daemon reuploads the exact digest.
CREATE TABLE role_source_artifact_integrity (
    workspace_id UUID NOT NULL,
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    storage_key TEXT NOT NULL CHECK (char_length(storage_key) BETWEEN 1 AND 1024),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'checking', 'healthy', 'quarantined')),
    last_outcome TEXT CHECK (last_outcome IN ('healthy', 'missing', 'size_mismatch', 'digest_mismatch', 'read_failed', 'reuploaded')),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    check_count BIGINT NOT NULL DEFAULT 0 CHECK (check_count >= 0),
    failure_count BIGINT NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    repair_count BIGINT NOT NULL DEFAULT 0 CHECK (repair_count >= 0),
    next_check_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_checked_at TIMESTAMPTZ,
    last_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (state = 'checking' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR
        (state <> 'checking' AND lease_token IS NULL AND lease_expires_at IS NULL)
    )
);
