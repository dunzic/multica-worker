-- Source-neutral control-plane records for pluggable role sources. Raw source
-- configuration and secret values are deliberately absent: the selected
-- daemon owns them and the server persists only a stable config reference plus
-- the adapter-provided redacted view.
--
-- Repository policy forbids foreign keys and non-concurrent index creation.
-- Relationships are checked by application transactions; every index is
-- created in its own following migration.
CREATE TABLE role_source (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{1,63}$'),
    adapter_version TEXT NOT NULL CHECK (char_length(adapter_version) BETWEEN 1 AND 100),
    daemon_config_id TEXT NOT NULL CHECK (char_length(daemon_config_id) BETWEEN 1 AND 512),
    config_redacted JSONB NOT NULL CHECK (jsonb_typeof(config_redacted) = 'object'),
    policy JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy) = 'object'),
    state TEXT NOT NULL DEFAULT 'registered' CHECK (state IN ('registered', 'active', 'paused', 'error', 'detached')),
    current_snapshot_digest TEXT CHECK (current_snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    audit_sequence BIGINT NOT NULL DEFAULT 0 CHECK (audit_sequence >= 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by UUID NOT NULL,
    updated_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_source_snapshot (
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_digest TEXT NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{1,63}$'),
    adapter_version TEXT NOT NULL CHECK (char_length(adapter_version) BETWEEN 1 AND 100),
    contract_version TEXT NOT NULL CHECK (char_length(contract_version) BETWEEN 1 AND 100),
    manifest JSONB NOT NULL CHECK (jsonb_typeof(manifest) = 'object'),
    diagnostics JSONB NOT NULL CHECK (jsonb_typeof(diagnostics) = 'array'),
    source_evidence JSONB NOT NULL CHECK (jsonb_typeof(source_evidence) = 'object'),
    reported_by_runtime_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Durable daemon work item. A scan request has its own identity and lease so
-- repeated scans that produce the same content-addressed snapshot remain
-- independently observable and retry-safe.
CREATE TABLE role_source_scan_request (
    id UUID NOT NULL,
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'claimed', 'succeeded', 'failed', 'cancelled')),
    requested_by UUID NOT NULL,
    expected_adapter_version TEXT NOT NULL CHECK (char_length(expected_adapter_version) BETWEEN 1 AND 100),
    claimed_by_runtime_id UUID,
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    snapshot_digest TEXT CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    error_code TEXT CHECK (error_code IS NULL OR char_length(error_code) BETWEEN 1 AND 200),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CHECK (
        (status = 'claimed' AND claimed_by_runtime_id IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR status <> 'claimed'
    ),
    CHECK (
        (status = 'succeeded' AND snapshot_digest IS NOT NULL AND completed_at IS NOT NULL)
        OR status <> 'succeeded'
    ),
    CHECK (
        (status IN ('failed', 'cancelled') AND completed_at IS NOT NULL)
        OR status NOT IN ('failed', 'cancelled')
    )
);

CREATE TABLE role_source_plan (
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    from_snapshot_digest TEXT CHECK (from_snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    to_snapshot_digest TEXT NOT NULL CHECK (to_snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan JSONB NOT NULL CHECK (jsonb_typeof(plan) = 'object'),
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Approval is append-only. A later decision supersedes an earlier decision;
-- plan JSON itself remains immutable and independently digest-verifiable.
CREATE TABLE role_source_plan_approval (
    id UUID NOT NULL,
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected', 'superseded')),
    decisions JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(decisions) = 'object'),
    actor_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_source_apply (
    id UUID NOT NULL,
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    request_key TEXT NOT NULL CHECK (char_length(request_key) BETWEEN 1 AND 200),
    mode TEXT NOT NULL CHECK (mode IN ('apply', 'rollback')),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'rejected')),
    actor_user_id UUID NOT NULL,
    receipt_digest TEXT CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    receipt JSONB CHECK (receipt IS NULL OR jsonb_typeof(receipt) = 'object'),
    error_code TEXT CHECK (error_code IS NULL OR char_length(error_code) BETWEEN 1 AND 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

-- Append-only lifecycle chain. audit_sequence is allocated while role_source
-- is locked, and event_digest covers the previous digest plus the event body.
CREATE TABLE role_source_audit_event (
    id UUID NOT NULL,
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (char_length(event_type) BETWEEN 1 AND 200),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'runtime', 'system')),
    actor_id UUID,
    previous_event_digest TEXT CHECK (previous_event_digest ~ '^sha256:[0-9a-f]{64}$'),
    event_digest TEXT NOT NULL CHECK (event_digest ~ '^sha256:[0-9a-f]{64}$'),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((actor_type = 'system' AND actor_id IS NULL) OR (actor_type <> 'system' AND actor_id IS NOT NULL))
);
