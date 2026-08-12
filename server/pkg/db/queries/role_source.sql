-- name: LockWorkspaceForRoleSourceMutation :one
-- Every role-source mutation takes this lock before any source/request lock.
-- Workspace teardown takes FOR UPDATE on the same row, preventing a mutation
-- from committing child data after the explicit no-FK cleanup sweep.
SELECT id FROM workspace WHERE id = @workspace_id FOR KEY SHARE;

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

-- name: GetRoleSourceArtifactForApply :one
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id AND digest = @digest
FOR SHARE;

-- name: ListRoleSourceArtifactsByDigests :many
SELECT * FROM role_source_artifact
WHERE workspace_id = @workspace_id
  AND digest = ANY(@digests::text[])
ORDER BY digest;

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

-- name: UpsertRoleSourceObjectMapping :one
INSERT INTO role_source_object_mapping (
    source_id, workspace_id, source_kind, source_parent_id, source_object_id,
    target_kind, target_id, ownership_mask, last_applied_digest,
    last_snapshot_digest, archived_at
) VALUES (
    @source_id, @workspace_id, @source_kind, @source_parent_id, @source_object_id,
    @target_kind, @target_id, @ownership_mask, @last_applied_digest,
    @last_snapshot_digest, sqlc.narg('archived_at')
)
ON CONFLICT (source_id, source_kind, source_parent_id, source_object_id)
DO UPDATE SET target_kind = EXCLUDED.target_kind,
              target_id = EXCLUDED.target_id,
              ownership_mask = EXCLUDED.ownership_mask,
              last_applied_digest = EXCLUDED.last_applied_digest,
              last_snapshot_digest = EXCLUDED.last_snapshot_digest,
              archived_at = EXCLUDED.archived_at,
              updated_at = now()
RETURNING *;

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
