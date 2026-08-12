-- Stable source-object identity and field ownership. The initial materializer
-- supports roles, role-owned skills and schedule automations. Capability
-- versions use the immutable catalog below; bindings, MCP and environment
-- declarations remain blockers until their dedicated domain contracts exist.
CREATE TABLE role_source_object_mapping (
    source_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('role', 'skill', 'automation')),
    source_parent_id TEXT NOT NULL DEFAULT '' CHECK (char_length(source_parent_id) <= 512),
    source_object_id TEXT NOT NULL CHECK (char_length(source_object_id) BETWEEN 1 AND 512),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('agent', 'skill', 'autopilot')),
    target_id UUID NOT NULL,
    ownership_mask JSONB NOT NULL CHECK (jsonb_typeof(ownership_mask) = 'array'),
    last_applied_digest TEXT NOT NULL CHECK (last_applied_digest ~ '^sha256:[0-9a-f]{64}$'),
    last_snapshot_digest TEXT NOT NULL CHECK (last_snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Immutable source-neutral capability versions are persisted even before a
-- runtime consumes them. Artifacts remain referenced by digest and are read
-- through the verified artifact ledger.
CREATE TABLE role_source_capability_version (
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    capability_id TEXT NOT NULL CHECK (char_length(capability_id) BETWEEN 1 AND 512),
    version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 200),
    object_digest TEXT NOT NULL CHECK (object_digest ~ '^sha256:[0-9a-f]{64}$'),
    definition JSONB NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
