CREATE TABLE role_source_apply_failure (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    approval_id UUID NOT NULL,
    actor_user_id UUID NOT NULL,
    request_key_digest TEXT NOT NULL CHECK (request_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    mode TEXT NOT NULL CHECK (mode IN ('apply', 'rollback', 'unknown')),
    failure_stage TEXT NOT NULL CHECK (failure_stage IN ('preflight', 'transaction', 'materialization', 'finalize', 'commit')),
    failure_code TEXT NOT NULL CHECK (char_length(failure_code) BETWEEN 1 AND 100),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
