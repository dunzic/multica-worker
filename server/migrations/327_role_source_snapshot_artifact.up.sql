-- Explicit reachability edges keep artifact retention independent from JSONB
-- traversal and let snapshot insertion fence a concurrent garbage collector.
-- Relationships are enforced by the report/teardown transactions, not FKs.
CREATE TABLE role_source_snapshot_artifact (
    workspace_id UUID NOT NULL,
    source_id UUID NOT NULL,
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
