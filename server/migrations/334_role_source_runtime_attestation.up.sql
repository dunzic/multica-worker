-- Current loaded-config evidence and a compact history of distinct evidence
-- states reported by each runtime. Both tables are deliberately source-neutral
-- and contain only runtime-scoped config-ID digests, adapter identities, and
-- content digests. Paths,
-- raw adapter configuration, allowed roots, and secrets are forbidden.
--
-- No foreign keys: workspace/runtime cleanup is explicit in application SQL.
CREATE TABLE role_source_runtime_attestation (
    runtime_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    contract_version TEXT NOT NULL CHECK (contract_version = 'role-source-config-attestation-v1'),
    loaded BOOLEAN NOT NULL,
    attestation_id TEXT NOT NULL CHECK (attestation_id ~ '^sha256:[0-9a-f]{64}$'),
    config_revision TEXT CHECK (config_revision IS NULL OR config_revision ~ '^sha256:[0-9a-f]{64}$'),
    sources JSONB NOT NULL CHECK (jsonb_typeof(sources) = 'array'),
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
        OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
    )
);

CREATE TABLE role_source_runtime_attestation_observation (
    runtime_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    contract_version TEXT NOT NULL CHECK (contract_version = 'role-source-config-attestation-v1'),
    loaded BOOLEAN NOT NULL,
    attestation_id TEXT NOT NULL CHECK (attestation_id ~ '^sha256:[0-9a-f]{64}$'),
    config_revision TEXT CHECK (config_revision IS NULL OR config_revision ~ '^sha256:[0-9a-f]{64}$'),
    sources JSONB NOT NULL CHECK (jsonb_typeof(sources) = 'array'),
    first_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    observation_count BIGINT NOT NULL DEFAULT 1 CHECK (observation_count > 0),
    CHECK (
        (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
        OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
    )
);
