-- One-time secret-transfer challenge. Private key material is encrypted with
-- the server master key and authenticated against the public claims. Submitted
-- envelopes remain ciphertext until an approved apply consumes them.
CREATE TABLE role_source_secret_transfer (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    plan_digest TEXT NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    approval_id UUID NOT NULL,
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    role_id TEXT NOT NULL CHECK (char_length(role_id) BETWEEN 1 AND 512),
    request_key TEXT NOT NULL CHECK (char_length(request_key) BETWEEN 1 AND 200),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'submitted', 'consumed', 'expired', 'failed')),
    public_key TEXT NOT NULL CHECK (char_length(public_key) = 43),
    private_key_ciphertext BYTEA NOT NULL CHECK (octet_length(private_key_ciphertext) BETWEEN 60 AND 256),
    key_id TEXT NOT NULL CHECK (char_length(key_id) BETWEEN 1 AND 100),
    claims JSONB NOT NULL CHECK (jsonb_typeof(claims) = 'object'),
    envelope JSONB CHECK (envelope IS NULL OR jsonb_typeof(envelope) = 'object'),
    envelope_digest TEXT CHECK (envelope_digest IS NULL OR envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
    claimed_by_runtime_id UUID,
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at TIMESTAMPTZ,
    submitted_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    error_code TEXT CHECK (error_code IS NULL OR char_length(error_code) BETWEEN 1 AND 200)
);
