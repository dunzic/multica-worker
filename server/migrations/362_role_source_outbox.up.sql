CREATE TABLE role_source_outbox (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('role_source:applied')),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'system')),
    actor_id UUID,
    apply_id UUID NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('apply', 'rollback')),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    receipt_digest TEXT NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'publishing', 'published', 'dead')),
    attempt SMALLINT NOT NULL DEFAULT 0 CHECK (attempt BETWEEN 0 AND 20),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 64),
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((actor_type = 'user') = (actor_id IS NOT NULL)),
    CHECK (
        (status = 'publishing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'publishing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CHECK ((status = 'published') = (published_at IS NOT NULL))
);
