-- Immutable, content-free proof that one deletion epoch completed every
-- permanent-purge/tombstone pass and ended with the exact key absent. It is
-- intentionally retained after workspace deletion. This is logical-storage
-- evidence, not a provider billing statement.
CREATE TABLE role_source_artifact_purge_receipt (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    intent_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    storage_key_digest TEXT NOT NULL CHECK (storage_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest TEXT NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
    reason TEXT NOT NULL CHECK (reason IN ('unreachable', 'workspace_deleted')),
    storage_backend TEXT NOT NULL CHECK (storage_backend IN ('local', 's3')),
    purge_mode TEXT NOT NULL CHECK (purge_mode IN ('current_object', 'all_versions')),
    successful_passes INTEGER NOT NULL CHECK (successful_passes BETWEEN 1 AND 100),
    deleted_versions BIGINT NOT NULL CHECK (deleted_versions >= 0),
    deleted_delete_markers BIGINT NOT NULL CHECK (deleted_delete_markers >= 0),
    observed_deleted_bytes BIGINT NOT NULL CHECK (observed_deleted_bytes >= 0),
    logical_bytes_confirmed_absent BIGINT NOT NULL CHECK (logical_bytes_confirmed_absent = size_bytes),
    absence_verified BOOLEAN NOT NULL CHECK (absence_verified),
    completed_at TIMESTAMPTZ NOT NULL,
    receipt_digest TEXT NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
