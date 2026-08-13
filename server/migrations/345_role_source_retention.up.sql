-- Append-only policy revisions make retention decisions auditable and
-- idempotent. Candidate rows separate preview/selection from destructive work
-- and carry only bounded metadata, never snapshot content.
CREATE TABLE role_source_retention_policy (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    request_key_digest TEXT NOT NULL CHECK (request_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    enabled BOOLEAN NOT NULL,
    minimum_age_days INTEGER NOT NULL CHECK (minimum_age_days BETWEEN 30 AND 3650),
    keep_successful_snapshots INTEGER NOT NULL CHECK (keep_successful_snapshots BETWEEN 2 AND 100),
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_source_retention_candidate (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    policy_version BIGINT NOT NULL CHECK (policy_version > 0),
    snapshot_created_at TIMESTAMPTZ NOT NULL,
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'claimed', 'completed')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    result_code TEXT CHECK (result_code IS NULL OR result_code IN (
        'pruned', 'policy_age', 'current_snapshot', 'legal_hold', 'task_pin', 'object_mapping',
        'active_transfer', 'active_apply', 'recent_plan', 'rollback_reserve',
        'policy_disabled', 'snapshot_missing', 'state_conflict', 'internal_failure'
    )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CHECK (
        (state = 'claimed' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (state <> 'claimed' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CHECK ((state = 'completed' AND completed_at IS NOT NULL AND result_code = 'pruned') OR state <> 'completed')
);
