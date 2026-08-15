-- Immutable execution provenance for tasks created against a source-managed
-- role. The queue remains the high-write lifecycle table; the optional pin is
-- kept separately so ordinary tasks pay no JSONB row-width cost. Relationships
-- are enforced by the trigger/application layer in accordance with the
-- repository's no-foreign-key policy.
CREATE TABLE role_source_task_pin (
    task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    source_id UUID NOT NULL,
    source_role_id TEXT NOT NULL CHECK (char_length(source_role_id) BETWEEN 1 AND 512),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    role_object_digest TEXT NOT NULL CHECK (role_object_digest ~ '^sha256:[0-9a-f]{64}$'),
    target_state_digest TEXT NOT NULL CHECK (target_state_digest ~ '^sha256:[0-9a-f]{64}$'),
    capability_pins JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(capability_pins) = 'array'),
    inherited_from_task_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
