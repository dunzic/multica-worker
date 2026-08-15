ALTER TABLE role_source_artifact_delete_intent
    DROP COLUMN IF EXISTS last_purged_at,
    DROP COLUMN IF EXISTS absence_verified,
    DROP COLUMN IF EXISTS observed_deleted_bytes,
    DROP COLUMN IF EXISTS deleted_delete_markers,
    DROP COLUMN IF EXISTS deleted_versions,
    DROP COLUMN IF EXISTS purge_passes,
    DROP COLUMN IF EXISTS purge_mode,
    DROP COLUMN IF EXISTS purge_backend,
    DROP COLUMN IF EXISTS workspace_id,
    DROP COLUMN IF EXISTS id;
