-- Append-only legal holds fence future historical retention and workspace
-- teardown. Human case references are never stored directly: callers may
-- provide only a SHA-256 commitment to an approved external record.
-- Releases are separate rows so a hold's original authority is immutable.
CREATE TABLE role_source_legal_hold (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    request_key_digest TEXT NOT NULL CHECK (request_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    scope TEXT NOT NULL CHECK (scope IN ('source', 'snapshot')),
    snapshot_digest TEXT CHECK (snapshot_digest IS NULL OR snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    reason_code TEXT NOT NULL CHECK (reason_code IN ('investigation', 'litigation', 'regulatory', 'customer_request', 'security_incident')),
    reference_digest TEXT CHECK (reference_digest IS NULL OR reference_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (scope = 'source' AND snapshot_digest IS NULL)
        OR (scope = 'snapshot' AND snapshot_digest IS NOT NULL)
    )
);

CREATE TABLE role_source_legal_hold_release (
    hold_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    request_key_digest TEXT NOT NULL CHECK (request_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    reason_code TEXT NOT NULL CHECK (reason_code IN ('resolved', 'court_order', 'entered_in_error', 'authorization_expired')),
    reference_digest TEXT CHECK (reference_digest IS NULL OR reference_digest ~ '^sha256:[0-9a-f]{64}$'),
    released_by UUID NOT NULL,
    released_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
