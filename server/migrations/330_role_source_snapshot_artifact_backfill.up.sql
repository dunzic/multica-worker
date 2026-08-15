-- Backfill every exact ArtifactRef location in the v1 normalized manifest.
-- UNION ALL is intentional; the unique index canonicalizes a digest reused by
-- several roles while preserving the one authoritative size for that digest.
INSERT INTO role_source_snapshot_artifact (
    workspace_id, source_id, snapshot_digest, artifact_digest, size_bytes
)
SELECT snapshot.workspace_id,
       snapshot.source_id,
       snapshot.snapshot_digest,
       ref.value->>'digest',
       (ref.value->>'size_bytes')::bigint
FROM role_source_snapshot snapshot
CROSS JOIN LATERAL (
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.capabilities[*].entrypoint') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.capabilities[*].artifacts[*]') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.roles[*].instructions') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.roles[*].profile') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.roles[*].skills[*].entrypoint') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.roles[*].skills[*].artifacts[*]') AS item(value)
    UNION ALL
    SELECT item.value FROM jsonb_path_query(snapshot.manifest, '$.roles[*].automations[*].prompt') AS item(value)
) AS ref
WHERE ref.value IS NOT NULL
  AND ref.value->>'digest' ~ '^sha256:[0-9a-f]{64}$'
  AND ref.value->>'size_bytes' ~ '^[0-9]+$'
ON CONFLICT (source_id, snapshot_digest, artifact_digest) DO NOTHING;
