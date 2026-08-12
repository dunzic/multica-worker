-- name: LockWorkspaceForRoleSourceMutation :one
-- Every role-source mutation takes this lock before any source/request lock.
-- Workspace teardown takes FOR UPDATE on the same row, preventing a mutation
-- from committing child data after the explicit no-FK cleanup sweep.
SELECT id FROM workspace WHERE id = @workspace_id FOR KEY SHARE;

-- name: LockRoleSourceRuntimeForRegistration :one
-- Runtime deletion takes FOR UPDATE before checking role_source references.
-- This shared lock prevents a source from appearing after that check and
-- becoming orphaned when the runtime row is removed.
SELECT id FROM agent_runtime
WHERE id = @runtime_id AND workspace_id = @workspace_id
FOR KEY SHARE;

-- name: RecordRoleSourceRuntimeAttestation :one
-- The daemon sends this only until the server acknowledges attestation_id, so
-- this is a process-start/config-change write rather than a heartbeat hot-path
-- write. Current evidence is replaced atomically; the observation catalog
-- retains every distinct state and counts repeated process observations.
WITH current_evidence AS (
    INSERT INTO role_source_runtime_attestation (
        runtime_id, workspace_id, contract_version, loaded,
        attestation_id, config_revision, sources
    ) VALUES (
        @runtime_id, @workspace_id, @contract_version, @loaded,
        @attestation_id, sqlc.narg('config_revision')::text, @sources
    )
    ON CONFLICT (runtime_id) DO UPDATE SET
        workspace_id = EXCLUDED.workspace_id,
        contract_version = EXCLUDED.contract_version,
        loaded = EXCLUDED.loaded,
        attestation_id = EXCLUDED.attestation_id,
        config_revision = EXCLUDED.config_revision,
        sources = EXCLUDED.sources,
        observed_at = now(),
        changed_at = CASE
            WHEN role_source_runtime_attestation.attestation_id IS DISTINCT FROM EXCLUDED.attestation_id
            THEN now()
            ELSE role_source_runtime_attestation.changed_at
        END
    RETURNING *
), observation AS (
    INSERT INTO role_source_runtime_attestation_observation (
        runtime_id, workspace_id, contract_version, loaded,
        attestation_id, config_revision, sources
    )
    SELECT runtime_id, workspace_id, contract_version, loaded,
           attestation_id, config_revision, sources
    FROM current_evidence
    ON CONFLICT (runtime_id, attestation_id) DO UPDATE SET
        workspace_id = EXCLUDED.workspace_id,
        last_observed_at = now(),
        observation_count = role_source_runtime_attestation_observation.observation_count + 1
)
SELECT * FROM current_evidence;

-- name: ListRoleSourceRuntimeAttestations :many
SELECT * FROM role_source_runtime_attestation
WHERE workspace_id = @workspace_id
  AND runtime_id = ANY(@runtime_ids::uuid[])
ORDER BY runtime_id;

-- name: ListRoleSourceRuntimeAttestationObservations :many
SELECT * FROM role_source_runtime_attestation_observation
WHERE workspace_id = @workspace_id AND runtime_id = @runtime_id
ORDER BY last_observed_at DESC, attestation_id
LIMIT @result_limit;

-- name: CreateRoleSource :one
INSERT INTO role_source (
    id, workspace_id, runtime_id, name, kind, adapter_version,
    daemon_config_id, config_redacted, policy, created_by, updated_by
) VALUES (
    @id, @workspace_id, @runtime_id, @name, @kind, @adapter_version,
    @daemon_config_id, @config_redacted, @policy, @actor_user_id, @actor_user_id
)
RETURNING *;

-- name: GetRoleSourceInWorkspace :one
SELECT * FROM role_source
WHERE id = @id AND workspace_id = @workspace_id;

-- name: GetRoleSourceForUpdate :one
SELECT * FROM role_source
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: ListRoleSourcesInWorkspace :many
SELECT * FROM role_source
WHERE workspace_id = @workspace_id
ORDER BY created_at DESC, id;

-- name: CountRoleSourcesByRuntime :one
SELECT count(*) FROM role_source WHERE runtime_id = @runtime_id;

-- name: CountRoleSourcesByRuntimes :one
SELECT count(*) FROM role_source WHERE runtime_id = ANY(@runtime_ids::uuid[]);

-- name: ReassignRoleSourcesToRuntime :execrows
UPDATE role_source
SET runtime_id = @new_runtime_id,
    version = version + 1,
    updated_at = now()
WHERE runtime_id = @old_runtime_id;

-- name: UpdateRoleSourceState :one
UPDATE role_source
SET state = @state,
    updated_by = @actor_user_id,
    version = version + 1,
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND version = @expected_version
RETURNING *;

-- name: AdvanceRoleSourceSnapshot :one
UPDATE role_source
SET current_snapshot_digest = @snapshot_digest,
    state = 'active',
    updated_by = @actor_user_id,
    version = version + 1,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND current_snapshot_digest IS NOT DISTINCT FROM sqlc.narg('expected_snapshot_digest')::text
RETURNING *;

-- name: AllocateRoleSourceAuditSequence :one
UPDATE role_source
SET audit_sequence = audit_sequence + 1,
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
RETURNING audit_sequence;

-- name: InsertRoleSourceSnapshot :one
INSERT INTO role_source_snapshot (
    source_id, workspace_id, snapshot_digest, manifest_digest,
    kind, adapter_version, contract_version, manifest, diagnostics,
    source_evidence, reported_by_runtime_id
) VALUES (
    @source_id, @workspace_id, @snapshot_digest, @manifest_digest,
    @kind, @adapter_version, @contract_version, @manifest, @diagnostics,
    @source_evidence, @reported_by_runtime_id
)
ON CONFLICT (source_id, snapshot_digest) DO NOTHING
RETURNING *;

-- name: GetRoleSourceSnapshot :one
SELECT * FROM role_source_snapshot
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND snapshot_digest = @snapshot_digest;

-- name: ListRoleSourceSnapshots :many
SELECT * FROM role_source_snapshot
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY created_at DESC, snapshot_digest
LIMIT @result_limit;

-- name: InsertRoleSourceArtifact :one
INSERT INTO role_source_artifact (
    workspace_id, digest, size_bytes, storage_key, uploaded_by_runtime_id,
    first_source_id, first_scan_request_id
) VALUES (
    @workspace_id, @digest, @size_bytes, @storage_key, @uploaded_by_runtime_id,
    @first_source_id, @first_scan_request_id
)
ON CONFLICT (workspace_id, digest) DO NOTHING
RETURNING *;

-- name: GetRoleSourceArtifact :one
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id AND digest = @digest;

-- name: ListRoleSourceArtifactsByDigests :many
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id
  AND digest = ANY(@digests::text[])
ORDER BY digest;

-- name: ListRoleSourceArtifactsForApplyByDigests :many
-- Apply preloads verified bodies before its mutation transaction, then takes
-- short shared ledger locks in one batch so concurrent retention/GC cannot
-- remove or retarget those digests before commit.
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id
  AND digest = ANY(@digests::text[])
ORDER BY digest
FOR SHARE;

-- name: ListRoleSourceArtifactsForSnapshotByDigests :many
-- Snapshot acceptance locks every ready body before it publishes reachability
-- edges. A collector uses FOR UPDATE SKIP LOCKED, so exactly one side wins:
-- either the snapshot commits its edge or the report observes the body absent.
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id
  AND digest = ANY(@digests::text[])
ORDER BY digest
FOR SHARE;

-- name: InsertRoleSourceSnapshotArtifacts :execrows
INSERT INTO role_source_snapshot_artifact (
    workspace_id, source_id, snapshot_digest, artifact_digest, size_bytes
)
SELECT @workspace_id, @source_id, @snapshot_digest, digests.artifact_digest, sizes.size_bytes
FROM unnest(@artifact_digests::text[]) WITH ORDINALITY AS digests(artifact_digest, position)
JOIN unnest(@artifact_sizes::bigint[]) WITH ORDINALITY AS sizes(size_bytes, position) USING (position)
ON CONFLICT (source_id, snapshot_digest, artifact_digest) DO NOTHING;

-- name: DeleteRoleSourceSnapshotArtifacts :exec
DELETE FROM role_source_snapshot_artifact
WHERE workspace_id = @workspace_id
  AND source_id = @source_id
  AND snapshot_digest = @snapshot_digest;

-- name: ListRoleSourceSnapshotArtifacts :many
SELECT * FROM role_source_snapshot_artifact
WHERE workspace_id = @workspace_id
  AND source_id = @source_id
  AND snapshot_digest = @snapshot_digest
ORDER BY artifact_digest;

-- name: QueueNextUnreachableRoleSourceArtifact :one
WITH candidate AS MATERIALIZED (
    SELECT artifact.*
    FROM role_source_artifact artifact
    WHERE artifact.created_at < now() - @settle_delay::interval
      AND NOT EXISTS (
          SELECT 1 FROM role_source_snapshot_artifact edge
          WHERE edge.workspace_id = artifact.workspace_id
            AND edge.artifact_digest = artifact.digest
      )
    ORDER BY artifact.created_at, artifact.storage_key
    FOR UPDATE OF artifact SKIP LOCKED
    LIMIT 1
), queued AS (
    INSERT INTO role_source_artifact_delete_intent (
        storage_key, artifact_digest, size_bytes, reason
    )
    SELECT storage_key, digest, size_bytes, 'unreachable' FROM candidate
    ON CONFLICT (storage_key) DO NOTHING
    RETURNING *
), removed AS (
    DELETE FROM role_source_artifact artifact
    USING queued
    WHERE artifact.storage_key = queued.storage_key
    RETURNING artifact.storage_key
)
SELECT queued.* FROM queued JOIN removed USING (storage_key);

-- name: ClaimNextRoleSourceArtifactDeleteIntent :one
WITH candidate AS MATERIALIZED (
    SELECT storage_key
    FROM role_source_artifact_delete_intent
    WHERE next_attempt_at <= now()
      AND (state IN ('pending', 'tombstoned') OR (state = 'deleting' AND lease_expires_at < now()))
    ORDER BY next_attempt_at, storage_key
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_artifact_delete_intent intent
SET state = 'deleting', lease_token = @lease_token,
    lease_expires_at = now() + @lease_duration::interval,
    attempt = attempt + 1, updated_at = now()
FROM candidate
WHERE intent.storage_key = candidate.storage_key
RETURNING intent.*;

-- name: TombstoneRoleSourceArtifactDeleteIntent :execrows
UPDATE role_source_artifact_delete_intent
SET state = 'tombstoned', lease_token = NULL, lease_expires_at = NULL,
    tombstone_pass = @tombstone_pass,
    next_attempt_at = now() + @next_delay::interval, updated_at = now()
WHERE storage_key = @storage_key AND state = 'deleting' AND lease_token = @lease_token;

-- name: ReleaseRoleSourceArtifactDeleteIntent :execrows
UPDATE role_source_artifact_delete_intent
SET state = 'pending', lease_token = NULL, lease_expires_at = NULL,
    next_attempt_at = now() + @retry_delay::interval, updated_at = now()
WHERE storage_key = @storage_key AND state = 'deleting' AND lease_token = @lease_token;

-- name: CompleteRoleSourceArtifactDeleteIntent :execrows
DELETE FROM role_source_artifact_delete_intent
WHERE storage_key = @storage_key AND state = 'deleting' AND lease_token = @lease_token;

-- name: GetRoleSourceArtifactDeleteIntentForUpdate :one
SELECT * FROM role_source_artifact_delete_intent
WHERE storage_key = @storage_key
FOR UPDATE;

-- name: CancelRoleSourceArtifactDeleteIntent :execrows
DELETE FROM role_source_artifact_delete_intent
WHERE storage_key = @storage_key AND state IN ('pending', 'tombstoned');

-- name: CountRoleSourceArtifactDeleteIntents :one
SELECT
    count(*) FILTER (WHERE state IN ('pending', 'deleting'))::bigint AS active_count,
    count(*) FILTER (WHERE state = 'tombstoned')::bigint AS tombstone_count
FROM role_source_artifact_delete_intent;

-- name: CreateRoleSourceScanRequest :one
INSERT INTO role_source_scan_request (
    id, source_id, workspace_id, requested_by, expected_adapter_version
) VALUES (
    @id, @source_id, @workspace_id, @requested_by, @expected_adapter_version
)
RETURNING *;

-- name: GetRoleSourceScanRequest :one
SELECT * FROM role_source_scan_request
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id;

-- name: GetRoleSourceScanRequestForUpdate :one
SELECT * FROM role_source_scan_request
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: ClaimNextRoleSourceScan :one
WITH source_candidate AS MATERIALIZED (
    SELECT source.id
    FROM role_source source
    WHERE source.runtime_id = @runtime_id
      AND source.state IN ('registered', 'active', 'error')
      AND EXISTS (
          SELECT 1
          FROM role_source_scan_request pending
          WHERE pending.source_id = source.id
            AND (
                pending.status = 'queued'
                OR (pending.status = 'claimed' AND pending.lease_expires_at < now())
            )
      )
    ORDER BY source.id
    FOR UPDATE OF source SKIP LOCKED
    LIMIT 1
), candidate AS (
    SELECT request.id
    FROM role_source_scan_request request
    JOIN source_candidate source ON source.id = request.source_id
    WHERE (
          request.status = 'queued'
          OR (request.status = 'claimed' AND request.lease_expires_at < now())
      )
    ORDER BY request.requested_at, request.id
    FOR UPDATE OF request SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_scan_request request
SET status = 'claimed',
    claimed_by_runtime_id = @runtime_id,
    lease_token = @lease_token,
    lease_expires_at = now() + @lease_duration::interval,
    claimed_at = COALESCE(claimed_at, now()),
    error_code = NULL
FROM candidate
WHERE request.id = candidate.id
RETURNING request.*;

-- name: RenewRoleSourceScanLease :one
UPDATE role_source_scan_request
SET lease_expires_at = now() + @lease_duration::interval
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND claimed_by_runtime_id = @runtime_id
  AND lease_token = @lease_token
  AND status = 'claimed'
  AND lease_expires_at > now()
RETURNING *;

-- name: CompleteRoleSourceScanSuccess :one
UPDATE role_source_scan_request
SET status = 'succeeded',
    snapshot_digest = @snapshot_digest,
    completed_at = now()
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND claimed_by_runtime_id = @runtime_id
  AND lease_token = @lease_token
  AND status = 'claimed'
  AND lease_expires_at > now()
RETURNING *;

-- name: CompleteRoleSourceScanFailure :one
UPDATE role_source_scan_request
SET status = 'failed',
    error_code = @error_code,
    completed_at = now()
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND claimed_by_runtime_id = @runtime_id
  AND lease_token = @lease_token
  AND status = 'claimed'
  AND lease_expires_at > now()
RETURNING *;

-- name: InsertRoleSourcePlan :one
INSERT INTO role_source_plan (
    source_id, workspace_id, plan_digest, from_snapshot_digest,
    to_snapshot_digest, plan, created_by
) VALUES (
    @source_id, @workspace_id, @plan_digest, sqlc.narg('from_snapshot_digest'),
    @to_snapshot_digest, @plan, @created_by
)
ON CONFLICT (source_id, plan_digest) DO NOTHING
RETURNING *;

-- name: GetRoleSourcePlan :one
SELECT * FROM role_source_plan
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest;

-- name: ListRoleSourcePlans :many
SELECT * FROM role_source_plan
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY created_at DESC, plan_digest
LIMIT @result_limit;

-- name: InsertRoleSourcePlanApproval :one
INSERT INTO role_source_plan_approval (
    id, source_id, workspace_id, plan_digest, request_key, decision, decisions, actor_user_id
) VALUES (
    @id, @source_id, @workspace_id, @plan_digest, @request_key, @decision, @decisions, @actor_user_id
)
ON CONFLICT (source_id, request_key) DO NOTHING
RETURNING *;

-- name: GetRoleSourcePlanApprovalByRequest :one
SELECT * FROM role_source_plan_approval
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND request_key = @request_key;

-- name: GetLatestRoleSourcePlanApproval :one
SELECT * FROM role_source_plan_approval
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetRoleSourcePlanApprovalByID :one
SELECT * FROM role_source_plan_approval
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest;

-- name: ListRoleSourcePlanApprovals :many
SELECT * FROM role_source_plan_approval
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest
ORDER BY created_at DESC, id DESC
LIMIT @result_limit;

-- name: ListRoleSourceObjectMappingsForUpdate :many
SELECT * FROM role_source_object_mapping
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY source_kind, source_parent_id, source_object_id
FOR UPDATE;

-- name: CountInvalidRoleSourceObjectMappings :one
-- Mapping rows intentionally have no foreign keys. Revalidate tenant and target
-- kind before trusting any target identity in a materialization transaction.
SELECT count(*) FROM role_source_object_mapping mapping
WHERE mapping.source_id = @source_id
  AND mapping.workspace_id = @workspace_id
  AND (
    (mapping.target_kind = 'agent' AND NOT EXISTS (
      SELECT 1 FROM agent target
      WHERE target.id = mapping.target_id AND target.workspace_id = mapping.workspace_id AND target.kind = 'user'
    ))
    OR (mapping.target_kind = 'skill' AND NOT EXISTS (
      SELECT 1 FROM skill target
      WHERE target.id = mapping.target_id AND target.workspace_id = mapping.workspace_id
    ))
    OR (mapping.target_kind = 'autopilot' AND NOT EXISTS (
      SELECT 1 FROM autopilot target
      WHERE target.id = mapping.target_id AND target.workspace_id = mapping.workspace_id
    ))
  );

-- name: FindRoleSourceAgentNameConflict :one
SELECT id FROM agent
WHERE workspace_id = @workspace_id AND name = @name AND kind = 'user'
LIMIT 1;

-- name: FindRoleSourceSkillNameConflict :one
SELECT id FROM skill
WHERE workspace_id = @workspace_id AND name = @name
LIMIT 1;

-- name: FindRoleSourceAutopilotTitleConflict :one
SELECT id FROM autopilot
WHERE workspace_id = @workspace_id AND title = @title AND status <> 'archived'
LIMIT 1;

-- name: UpsertRoleSourceObjectMappings :many
-- One apply can materialize thousands of objects. Send mapping mutations as a
-- single typed JSON recordset so the transaction does not pay one network
-- round trip per object. Row triggers still execute for every affected mapping.
WITH input AS (
    SELECT *
    FROM jsonb_to_recordset(@mappings::jsonb) AS item(
        source_kind TEXT,
        source_parent_id TEXT,
        source_object_id TEXT,
        target_kind TEXT,
        target_id UUID,
        ownership_mask JSONB,
        last_applied_digest TEXT,
        last_snapshot_digest TEXT,
        archived_at TIMESTAMPTZ
    )
)
INSERT INTO role_source_object_mapping (
    source_id, workspace_id, source_kind, source_parent_id, source_object_id,
    target_kind, target_id, ownership_mask, last_applied_digest,
    last_snapshot_digest, archived_at
)
SELECT
    @source_id, @workspace_id, source_kind, source_parent_id, source_object_id,
    target_kind, target_id, ownership_mask, last_applied_digest,
    last_snapshot_digest, archived_at
FROM input
ON CONFLICT (source_id, source_kind, source_parent_id, source_object_id)
DO UPDATE SET target_kind = EXCLUDED.target_kind,
              target_id = EXCLUDED.target_id,
              ownership_mask = EXCLUDED.ownership_mask,
              last_applied_digest = EXCLUDED.last_applied_digest,
              last_snapshot_digest = EXCLUDED.last_snapshot_digest,
              archived_at = EXCLUDED.archived_at,
              updated_at = now()
RETURNING role_source_object_mapping.*;

-- name: InsertRoleSourceCapabilityVersion :one
INSERT INTO role_source_capability_version (
    workspace_id, source_id, capability_id, version, object_digest,
    definition, snapshot_digest
) VALUES (
    @workspace_id, @source_id, @capability_id, @version, @object_digest,
    @definition, @snapshot_digest
)
ON CONFLICT (source_id, capability_id, version, object_digest) DO NOTHING
RETURNING *;

-- name: GetRoleSourceCapabilityVersion :one
SELECT * FROM role_source_capability_version
WHERE source_id = @source_id
  AND capability_id = @capability_id
  AND version = @version
  AND object_digest = @object_digest;

-- name: GetRoleSourceTaskPin :one
SELECT * FROM role_source_task_pin
WHERE task_id = @task_id;

-- name: ListRoleSourceTaskPins :many
SELECT * FROM role_source_task_pin
WHERE workspace_id = @workspace_id
  AND source_id = @source_id
  AND (
      sqlc.narg('before_created_at')::timestamptz IS NULL
      OR (created_at, task_id) < (sqlc.narg('before_created_at')::timestamptz, sqlc.narg('before_task_id')::uuid)
  )
ORDER BY created_at DESC, task_id DESC
LIMIT @result_limit;

-- name: ListRoleSourceRoleImpactRows :many
-- A plan impact preview is point-in-time operational evidence, not part of the
-- immutable plan digest. Count only tasks pinned to the mapping's current
-- source version: older terminal history must not inflate the apply impact.
WITH active_task_pin AS MATERIALIZED (
    SELECT
        pin.source_role_id,
        pin.agent_id,
        pin.snapshot_digest,
        pin.role_object_digest,
        task.id AS task_id,
        task.status,
        task.created_at
    FROM agent_task_queue task
    JOIN role_source_task_pin pin ON pin.task_id = task.id
    WHERE pin.workspace_id = @workspace_id
      AND pin.source_id = @source_id
      AND pin.source_role_id = ANY(@source_role_ids::text[])
      AND task.status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory')
)
SELECT
    mapping.source_object_id AS source_role_id,
    mapping.target_id AS agent_id,
    target.name AS agent_name,
    mapping.last_snapshot_digest,
    count(active.task_id) FILTER (
        WHERE active.status IN ('queued', 'deferred', 'dispatched')
    )::bigint AS cancel_on_apply,
    count(active.task_id) FILTER (
        WHERE active.status IN ('running', 'waiting_local_directory')
    )::bigint AS continue_current_version
FROM role_source_object_mapping mapping
JOIN agent target
  ON target.id = mapping.target_id
 AND target.workspace_id = mapping.workspace_id
 AND target.kind = 'user'
LEFT JOIN active_task_pin active
  ON active.source_role_id = mapping.source_object_id
 AND active.agent_id = mapping.target_id
 AND active.snapshot_digest = mapping.last_snapshot_digest
 AND active.role_object_digest = mapping.last_applied_digest
WHERE mapping.workspace_id = @workspace_id
  AND mapping.source_id = @source_id
  AND mapping.source_kind = 'role'
  AND mapping.source_parent_id = ''
  AND mapping.target_kind = 'agent'
  AND mapping.archived_at IS NULL
  AND mapping.source_object_id = ANY(@source_role_ids::text[])
GROUP BY mapping.source_object_id, mapping.target_id, target.name, mapping.last_snapshot_digest
ORDER BY mapping.source_object_id;

-- name: ListRoleSourceTaskImpactRows :many
-- Details are bounded separately from aggregate counts. No issue content,
-- prompts, results, errors or secret-bearing context cross this audit API.
WITH active_task_pin AS MATERIALIZED (
    SELECT
        pin.task_id,
        pin.source_role_id,
        pin.agent_id,
        pin.snapshot_digest,
        pin.role_object_digest,
        task.status,
        task.created_at
    FROM agent_task_queue task
    JOIN role_source_task_pin pin ON pin.task_id = task.id
    WHERE pin.workspace_id = @workspace_id
      AND pin.source_id = @source_id
      AND pin.source_role_id = ANY(@source_role_ids::text[])
      AND task.status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory')
)
SELECT
    active.task_id,
    active.source_role_id,
    active.agent_id,
    active.status,
    active.created_at
FROM role_source_object_mapping mapping
JOIN active_task_pin active
  ON active.source_role_id = mapping.source_object_id
 AND active.agent_id = mapping.target_id
 AND active.snapshot_digest = mapping.last_snapshot_digest
 AND active.role_object_digest = mapping.last_applied_digest
WHERE mapping.workspace_id = @workspace_id
  AND mapping.source_id = @source_id
  AND mapping.source_kind = 'role'
  AND mapping.source_parent_id = ''
  AND mapping.target_kind = 'agent'
  AND mapping.archived_at IS NULL
  AND mapping.source_object_id = ANY(@source_role_ids::text[])
  AND (
      mapping.last_snapshot_digest <> @target_snapshot_digest
      OR mapping.source_object_id = ANY(@conditional_archive_role_ids::text[])
  )
ORDER BY active.created_at DESC, active.task_id DESC
LIMIT @result_limit;

-- name: IsRoleSourceTaskPinCurrent :one
-- A claim must never silently execute the mutable agent row after a later
-- source apply changed its managed fields. The task keeps its immutable pin;
-- the caller fails closed and asks for an explicit new run when this returns
-- false. Locking the mapping serializes final claim commit with a concurrent
-- source apply; both paths then lock the task row in the same mapping -> task
-- order, avoiding a stale-delivery window and lock-order deadlocks.
WITH locked_mapping AS MATERIALIZED (
    SELECT mapping.*
    FROM role_source_task_pin pin
    JOIN role_source_object_mapping mapping
      ON mapping.workspace_id = pin.workspace_id
     AND mapping.source_id = pin.source_id
     AND mapping.source_kind = 'role'
     AND mapping.source_parent_id = ''
     AND mapping.source_object_id = pin.source_role_id
     AND mapping.target_kind = 'agent'
     AND mapping.target_id = pin.agent_id
    WHERE pin.task_id = @task_id
    FOR UPDATE OF mapping
)
SELECT EXISTS (
    SELECT 1
    FROM role_source_task_pin pin
    JOIN locked_mapping mapping
      ON mapping.workspace_id = pin.workspace_id
     AND mapping.source_id = pin.source_id
     AND mapping.archived_at IS NULL
     AND mapping.last_snapshot_digest = pin.snapshot_digest
     AND mapping.last_applied_digest = pin.role_object_digest
    WHERE pin.task_id = @task_id
      AND pin.target_state_digest = role_source_agent_state_digest(
          pin.agent_id, pin.source_id, pin.source_role_id
      )
      AND EXISTS (
          SELECT 1
          FROM role_source_snapshot snapshot,
               jsonb_array_elements(snapshot.manifest->'roles') AS role
          WHERE snapshot.workspace_id = pin.workspace_id
            AND snapshot.source_id = pin.source_id
            AND snapshot.snapshot_digest = pin.snapshot_digest
            AND role->>'id' = pin.source_role_id
      )
);

-- name: InsertRoleSourceSecretTransfer :one
INSERT INTO role_source_secret_transfer (
    id, workspace_id, source_id, runtime_id, plan_digest, approval_id,
    snapshot_digest, role_id, request_key, public_key,
    private_key_ciphertext, key_id, claims, expires_at, created_by
) VALUES (
    @id, @workspace_id, @source_id, @runtime_id, @plan_digest, @approval_id,
    @snapshot_digest, @role_id, @request_key, @public_key,
    @private_key_ciphertext, @key_id, @claims, @expires_at, @created_by
)
ON CONFLICT (source_id, request_key) DO NOTHING
RETURNING *;

-- name: GetRoleSourceSecretTransferByRequest :one
SELECT * FROM role_source_secret_transfer
WHERE source_id = @source_id AND workspace_id = @workspace_id AND request_key = @request_key;

-- name: GetRoleSourceSecretTransfer :one
SELECT * FROM role_source_secret_transfer
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id;

-- name: GetRoleSourceSecretTransferForUpdate :one
SELECT * FROM role_source_secret_transfer
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: GetRoleSourceSecretTransferForApply :one
SELECT * FROM role_source_secret_transfer
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest
  AND approval_id = @approval_id
  AND snapshot_digest = @snapshot_digest
  AND role_id = @role_id
  AND status = 'submitted'
  AND expires_at > now()
FOR UPDATE;

-- name: ClaimNextRoleSourceSecretTransfer :one
WITH candidate AS (
    SELECT id FROM role_source_secret_transfer
    WHERE runtime_id = @runtime_id
      AND expires_at > now()
      AND (
        status = 'pending'
        OR (status = 'claimed' AND lease_expires_at < now())
      )
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_secret_transfer transfer
SET status = 'claimed',
    claimed_by_runtime_id = @runtime_id,
    lease_token = @lease_token,
    lease_expires_at = LEAST(expires_at, now() + @lease_duration::interval),
    claimed_at = COALESCE(claimed_at, now()),
    error_code = NULL
FROM candidate
WHERE transfer.id = candidate.id
RETURNING transfer.*;

-- name: SubmitRoleSourceSecretTransfer :one
UPDATE role_source_secret_transfer
SET status = 'submitted', envelope = @envelope, envelope_digest = @envelope_digest,
    submitted_at = now(), lease_expires_at = NULL
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND claimed_by_runtime_id = @runtime_id
  AND lease_token = @lease_token
  AND status = 'claimed'
  AND expires_at > now()
  AND lease_expires_at > now()
RETURNING *;

-- name: FailRoleSourceSecretTransfer :one
UPDATE role_source_secret_transfer
SET status = 'failed', error_code = @error_code, lease_expires_at = NULL
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND claimed_by_runtime_id = @runtime_id
  AND lease_token = @lease_token
  AND status = 'claimed'
  AND expires_at > now()
  AND lease_expires_at > now()
RETURNING *;

-- name: ConsumeRoleSourceSecretTransfer :one
UPDATE role_source_secret_transfer
SET status = 'consumed', envelope = NULL,
    private_key_ciphertext = decode(repeat('00', 60), 'hex'),
    lease_token = NULL, lease_expires_at = NULL, consumed_at = now()
WHERE id = @id
  AND source_id = @source_id
  AND workspace_id = @workspace_id
  AND status = 'submitted'
  AND expires_at > now()
RETURNING *;

-- name: ExpireRoleSourceSecretTransfers :many
WITH expired AS MATERIALIZED (
    SELECT id
    FROM role_source_secret_transfer
    WHERE expires_at <= now()
      AND status IN ('pending', 'claimed', 'submitted')
    ORDER BY expires_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT @batch_limit
)
UPDATE role_source_secret_transfer transfer
SET status = 'expired', envelope = NULL,
    private_key_ciphertext = decode(repeat('00', 60), 'hex'),
    lease_token = NULL, lease_expires_at = NULL, error_code = 'expired'
FROM expired
WHERE transfer.id = expired.id
RETURNING transfer.id;

-- name: InsertRoleSourceApply :one
INSERT INTO role_source_apply (
    id, source_id, workspace_id, request_key, mode, snapshot_digest,
    plan_digest, status, actor_user_id
) VALUES (
    @id, @source_id, @workspace_id, @request_key, @mode, @snapshot_digest,
    @plan_digest, 'pending', @actor_user_id
)
ON CONFLICT (source_id, request_key) DO NOTHING
RETURNING *;

-- name: GetRoleSourceApplyByRequest :one
SELECT * FROM role_source_apply
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND request_key = @request_key;

-- name: GetLatestSucceededRoleSourceApplyForSnapshot :one
SELECT * FROM role_source_apply
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND snapshot_digest = @snapshot_digest
  AND status = 'succeeded'
ORDER BY completed_at DESC, id DESC
LIMIT 1;

-- name: ListSucceededRoleSourceApplies :many
SELECT * FROM role_source_apply
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND status = 'succeeded'
ORDER BY completed_at DESC, id DESC
LIMIT @result_limit;

-- name: MarkRoleSourceApplyRunning :one
UPDATE role_source_apply
SET status = 'running'
WHERE id = @id AND workspace_id = @workspace_id AND status = 'pending'
RETURNING *;

-- name: CompleteRoleSourceApply :one
UPDATE role_source_apply
SET status = @status,
    receipt_digest = @receipt_digest,
    receipt = @receipt,
    error_code = sqlc.narg('error_code'),
    completed_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'running'
RETURNING *;

-- name: InsertRoleSourceApplyFailure :one
-- This append-only row is written after the main mutation transaction has
-- rolled back. It intentionally stores no request key, raw error or payload.
INSERT INTO role_source_apply_failure (
    id, workspace_id, source_id, plan_digest, approval_id, actor_user_id,
    request_key_digest, mode, failure_stage, failure_code, occurred_at
) VALUES (
    @id, @workspace_id, @source_id, @plan_digest, @approval_id, @actor_user_id,
    @request_key_digest, @mode, @failure_stage, @failure_code, @occurred_at
)
RETURNING *;

-- name: ListRoleSourceApplyFailures :many
SELECT * FROM role_source_apply_failure
WHERE workspace_id = @workspace_id AND source_id = @source_id
ORDER BY occurred_at DESC, id DESC
LIMIT @result_limit;

-- name: InsertRoleSourceAuditEvent :one
INSERT INTO role_source_audit_event (
    id, source_id, workspace_id, sequence, event_type, actor_type, actor_id,
    previous_event_digest, event_digest, payload, created_at
) VALUES (
    @id, @source_id, @workspace_id, @sequence, @event_type, @actor_type,
    sqlc.narg('actor_id'), sqlc.narg('previous_event_digest'), @event_digest, @payload, @occurred_at
)
RETURNING *;

-- name: GetLatestRoleSourceAuditEvent :one
SELECT * FROM role_source_audit_event
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY sequence DESC
LIMIT 1;

-- name: ListRoleSourceAuditEvents :many
SELECT * FROM role_source_audit_event
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND sequence < @before_sequence
ORDER BY sequence DESC
LIMIT @result_limit;
