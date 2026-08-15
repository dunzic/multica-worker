ALTER TABLE role_source_artifact_delete_intent
    ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN workspace_id UUID,
    ADD COLUMN purge_backend TEXT CHECK (purge_backend IS NULL OR purge_backend IN ('local', 's3')),
    ADD COLUMN purge_mode TEXT CHECK (purge_mode IS NULL OR purge_mode IN ('current_object', 'all_versions')),
    ADD COLUMN purge_passes INTEGER NOT NULL DEFAULT 0 CHECK (purge_passes BETWEEN 0 AND 100),
    ADD COLUMN deleted_versions BIGINT NOT NULL DEFAULT 0 CHECK (deleted_versions >= 0),
    ADD COLUMN deleted_delete_markers BIGINT NOT NULL DEFAULT 0 CHECK (deleted_delete_markers >= 0),
    ADD COLUMN observed_deleted_bytes BIGINT NOT NULL DEFAULT 0 CHECK (observed_deleted_bytes >= 0),
    ADD COLUMN absence_verified BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_purged_at TIMESTAMPTZ;

-- Existing role-source artifact keys are deterministic and tenant scoped. A
-- malformed legacy key aborts the NOT NULL transition instead of producing an
-- unattributable receipt.
UPDATE role_source_artifact_delete_intent
SET workspace_id = substring(storage_key FROM
    '^role-source-artifacts/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/[0-9a-f]{64}$')::uuid;

ALTER TABLE role_source_artifact_delete_intent
    ALTER COLUMN workspace_id SET NOT NULL;
