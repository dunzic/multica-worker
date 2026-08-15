-- name: LockWorkspaceForRoleSourceMutation :one
-- Every role-source mutation takes this lock before any source/request lock.
-- Workspace teardown takes FOR UPDATE on the same row, preventing a mutation
-- from committing child data after the explicit no-FK cleanup sweep.
SELECT id FROM workspace WHERE id = @workspace_id FOR KEY SHARE;

-- name: InsertRoleSourceOutboxEvent :one
INSERT INTO role_source_outbox (
    id, workspace_id, source_id, event_type, actor_type, actor_id,
    apply_id, mode, snapshot_digest, plan_digest, receipt_digest
) VALUES (
    @id, @workspace_id, @source_id, @event_type, @actor_type,
    sqlc.narg('actor_id')::uuid, @apply_id, @mode, @snapshot_digest,
    @plan_digest, @receipt_digest
)
RETURNING *;

-- name: GetRoleSourceOutboxForReplay :one
SELECT * FROM role_source_outbox
WHERE id = @id
FOR UPDATE;

-- name: GetRoleSourceOutboxByID :one
SELECT * FROM role_source_outbox
WHERE id = @id;

-- name: GetRoleSourceApplyForOutboxReplay :one
SELECT * FROM role_source_apply
WHERE id = @apply_id
  AND workspace_id = @workspace_id
  AND source_id = @source_id
  AND status = 'succeeded';

-- name: ListRoleSourceApplyAuditEventsForOutboxReplay :many
SELECT * FROM role_source_audit_event
WHERE workspace_id = @workspace_id
  AND source_id = @source_id
  AND event_type = @event_type
  AND payload ->> 'operation_id' = @apply_id::text
  AND payload ->> 'receipt_digest' = sqlc.arg('receipt_digest')::text
ORDER BY sequence DESC
LIMIT 2;

-- name: ListRoleSourceAuditChainForOutboxReplay :many
SELECT * FROM role_source_audit_event
WHERE workspace_id = @workspace_id
  AND source_id = @source_id
  AND sequence <= @through_sequence
ORDER BY sequence
LIMIT @result_limit;

-- name: GetLatestRoleSourceOutboxReplay :one
SELECT * FROM role_source_outbox_replay
WHERE outbox_id = @outbox_id
ORDER BY generation DESC
LIMIT 1;

-- name: GetRoleSourceOutboxReplayByAuthorization :one
SELECT * FROM role_source_outbox_replay
WHERE authorization_id = @authorization_id;

-- name: InsertRoleSourceOutboxReplay :one
INSERT INTO role_source_outbox_replay (
    id, outbox_id, workspace_id, source_id, apply_id, authorization_id, generation,
    reason_code, incident_reference_digest, requester_key_id,
    approver_key_id, authorization_digest, requester_signature_digest,
    approver_signature_digest, expected_receipt_digest,
    previous_replay_digest, replay_digest, created_at
) VALUES (
    @id, @outbox_id, @workspace_id, @source_id, @apply_id, @authorization_id, @generation,
    @reason_code, @incident_reference_digest, @requester_key_id,
    @approver_key_id, @authorization_digest, @requester_signature_digest,
    @approver_signature_digest, @expected_receipt_digest,
    sqlc.narg('previous_replay_digest')::text, @replay_digest, @created_at
)
RETURNING *;

-- name: RequeueDeadRoleSourceOutboxEvent :one
UPDATE role_source_outbox
SET status = 'pending', attempt = 0, lease_token = NULL,
    lease_expires_at = NULL, next_attempt_at = now(), last_error_code = NULL,
    published_at = NULL, replay_count = replay_count + 1,
    last_replayed_at = @replayed_at
WHERE id = @id
  AND status = 'dead'
  AND replay_count = @expected_replay_count
  AND replay_count < 3
RETURNING *;

-- name: ClaimNextRoleSourceOutboxEvent :one
WITH candidate AS (
    SELECT id
    FROM role_source_outbox
    WHERE status IN ('pending', 'publishing')
      AND attempt < 20
      AND next_attempt_at <= now()
      AND (status = 'pending' OR lease_expires_at <= now())
    ORDER BY next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_outbox outbox
SET status = 'publishing',
    attempt = outbox.attempt + 1,
    lease_token = @lease_token,
    lease_expires_at = now() + @lease_duration::interval,
    last_error_code = NULL
FROM candidate
WHERE outbox.id = candidate.id
RETURNING outbox.*;

-- name: MarkRoleSourceOutboxPublished :one
UPDATE role_source_outbox
SET status = 'published', lease_token = NULL, lease_expires_at = NULL,
    published_at = now(), last_error_code = NULL
WHERE id = @id AND status = 'publishing' AND lease_token = @lease_token
RETURNING *;

-- name: MarkExhaustedRoleSourceOutboxEventsDead :execrows
UPDATE role_source_outbox
SET status = 'dead', lease_token = NULL, lease_expires_at = NULL,
    last_error_code = COALESCE(last_error_code, 'lease_expired_after_max_attempts')
WHERE status = 'publishing' AND attempt >= 20 AND lease_expires_at <= now();

-- name: ReleaseRoleSourceOutboxEvent :one
UPDATE role_source_outbox
SET status = CASE WHEN attempt >= 20 THEN 'dead' ELSE 'pending' END,
    lease_token = NULL,
    lease_expires_at = NULL,
    next_attempt_at = now() + @retry_delay::interval,
    last_error_code = @last_error_code
WHERE id = @id AND status = 'publishing' AND lease_token = @lease_token
RETURNING *;

-- name: CountRoleSourceOutboxState :one
WITH active AS (
    SELECT count(*)::bigint AS active_count, min(created_at) AS oldest_created_at
    FROM role_source_outbox
    WHERE status IN ('pending', 'publishing')
), dead AS (
    SELECT count(*)::bigint AS dead_count
    FROM role_source_outbox
    WHERE status = 'dead'
)
SELECT active.active_count, dead.dead_count,
    COALESCE(EXTRACT(EPOCH FROM (now() - active.oldest_created_at)), 0)::bigint AS oldest_active_seconds
FROM active CROSS JOIN dead;

-- name: DeletePublishedRoleSourceOutboxEvents :execrows
WITH settled AS (
    SELECT id
    FROM role_source_outbox
    WHERE status = 'published'
      AND published_at < now() - interval '7 days'
      AND NOT EXISTS (
          SELECT 1 FROM role_source_outbox_replay replay
          WHERE replay.outbox_id = role_source_outbox.id
      )
    ORDER BY published_at, id
    LIMIT @delete_limit
)
DELETE FROM role_source_outbox outbox
USING settled
WHERE outbox.id = settled.id;

-- name: DeleteDeadRoleSourceOutboxEvents :execrows
WITH settled AS (
    SELECT id
    FROM role_source_outbox
    WHERE status = 'dead'
      AND created_at < now() - interval '30 days'
      AND NOT EXISTS (
          SELECT 1 FROM role_source_outbox_replay replay
          WHERE replay.outbox_id = role_source_outbox.id
      )
    ORDER BY created_at, id
    LIMIT @delete_limit
)
DELETE FROM role_source_outbox outbox
USING settled
WHERE outbox.id = settled.id;

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

-- name: GetRoleSourceRuntimeAttestationForShare :one
SELECT * FROM role_source_runtime_attestation
WHERE runtime_id = @runtime_id AND workspace_id = @workspace_id
FOR SHARE;

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

-- name: CreateRoleSourceLegalHold :one
INSERT INTO role_source_legal_hold (
    id, workspace_id, source_id, request_key_digest, scope, snapshot_digest,
    reason_code, reference_digest, created_by
) VALUES (
    @id, @workspace_id, @source_id, @request_key_digest, @scope,
    sqlc.narg('snapshot_digest')::text, @reason_code,
    sqlc.narg('reference_digest')::text, @created_by
)
RETURNING *;

-- name: GetRoleSourceLegalHoldByRequestKey :one
SELECT * FROM role_source_legal_hold
WHERE workspace_id = @workspace_id AND source_id = @source_id
  AND request_key_digest = @request_key_digest;

-- name: GetRoleSourceLegalHoldForUpdate :one
SELECT * FROM role_source_legal_hold
WHERE id = @id AND workspace_id = @workspace_id AND source_id = @source_id
FOR UPDATE;

-- name: CreateRoleSourceLegalHoldRelease :one
INSERT INTO role_source_legal_hold_release (
    hold_id, workspace_id, source_id, request_key_digest, reason_code,
    reference_digest, released_by
) VALUES (
    @hold_id, @workspace_id, @source_id, @request_key_digest, @reason_code,
    sqlc.narg('reference_digest')::text, @released_by
)
RETURNING *;

-- name: GetRoleSourceLegalHoldRelease :one
SELECT * FROM role_source_legal_hold_release
WHERE hold_id = @hold_id AND workspace_id = @workspace_id AND source_id = @source_id;

-- name: ListRoleSourceLegalHolds :many
SELECT
    hold.id, hold.workspace_id, hold.source_id, hold.scope,
    hold.snapshot_digest, hold.reason_code, hold.reference_digest,
    hold.created_by, hold.created_at,
    release.reason_code AS release_reason_code,
    release.reference_digest AS release_reference_digest,
    release.released_by, release.released_at
FROM role_source_legal_hold hold
LEFT JOIN role_source_legal_hold_release release
  ON release.hold_id = hold.id
WHERE hold.workspace_id = @workspace_id AND hold.source_id = @source_id
ORDER BY hold.created_at DESC, hold.id
LIMIT @result_limit;

-- name: CountActiveRoleSourceLegalHoldsInWorkspace :one
SELECT count(*) FROM role_source_legal_hold hold
WHERE hold.workspace_id = @workspace_id
  AND NOT EXISTS (
      SELECT 1 FROM role_source_legal_hold_release release
      WHERE release.hold_id = hold.id
  );

-- name: InsertRoleSourceRetentionPolicy :one
INSERT INTO role_source_retention_policy (
    id, workspace_id, source_id, version, request_key_digest, enabled,
    minimum_age_days, keep_successful_snapshots, created_by
) VALUES (
    @id, @workspace_id, @source_id, @version, @request_key_digest, @enabled,
    @minimum_age_days, @keep_successful_snapshots, @created_by
)
ON CONFLICT (source_id, request_key_digest) DO NOTHING
RETURNING *;

-- name: GetLatestRoleSourceRetentionPolicy :one
SELECT * FROM role_source_retention_policy
WHERE workspace_id = @workspace_id AND source_id = @source_id
ORDER BY version DESC
LIMIT 1;

-- name: GetRoleSourceRetentionPolicyByRequest :one
SELECT * FROM role_source_retention_policy
WHERE workspace_id = @workspace_id AND source_id = @source_id
  AND request_key_digest = @request_key_digest;

-- name: ListEligibleRoleSourceRetentionSnapshots :many
WITH successful AS MATERIALIZED (
    SELECT apply.snapshot_digest, max(apply.completed_at) AS applied_at
    FROM role_source_apply apply
    WHERE apply.workspace_id = @workspace_id AND apply.source_id = @source_id
      AND apply.status = 'succeeded'
    GROUP BY apply.snapshot_digest
), successful_rank AS MATERIALIZED (
    SELECT snapshot_digest, row_number() OVER (ORDER BY applied_at DESC, snapshot_digest DESC) AS reserve_rank
    FROM successful
), eligible AS MATERIALIZED (
SELECT snapshot.workspace_id, snapshot.source_id, snapshot.snapshot_digest, snapshot.created_at,
       COALESCE((SELECT sum(edge.size_bytes) FROM role_source_snapshot_artifact edge
                 WHERE edge.workspace_id = snapshot.workspace_id AND edge.source_id = snapshot.source_id
                   AND edge.snapshot_digest = snapshot.snapshot_digest), 0)::bigint AS estimated_bytes
FROM role_source_snapshot snapshot
WHERE snapshot.workspace_id = @workspace_id
  AND snapshot.source_id = @source_id
  AND snapshot.created_at < now() - make_interval(days => @minimum_age_days::int)
  AND snapshot.snapshot_digest IS DISTINCT FROM (
      SELECT current_snapshot_digest FROM role_source
      WHERE id = @source_id AND workspace_id = @workspace_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM successful_rank reserve_row
      WHERE reserve_row.snapshot_digest = snapshot.snapshot_digest
        AND reserve_row.reserve_rank <= sqlc.arg('keep_successful_snapshots')::int
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_legal_hold hold
      WHERE hold.workspace_id = snapshot.workspace_id
        AND hold.source_id = snapshot.source_id
        AND (hold.scope = 'source' OR hold.snapshot_digest = snapshot.snapshot_digest)
        AND NOT EXISTS (SELECT 1 FROM role_source_legal_hold_release release WHERE release.hold_id = hold.id)
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_task_pin pin
      WHERE pin.workspace_id = snapshot.workspace_id AND pin.source_id = snapshot.source_id
        AND pin.snapshot_digest = snapshot.snapshot_digest
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_object_mapping mapping
      WHERE mapping.workspace_id = snapshot.workspace_id AND mapping.source_id = snapshot.source_id
        AND mapping.last_snapshot_digest = snapshot.snapshot_digest
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_secret_transfer transfer
      WHERE transfer.workspace_id = snapshot.workspace_id AND transfer.source_id = snapshot.source_id
        AND transfer.snapshot_digest = snapshot.snapshot_digest
        AND transfer.status IN ('pending', 'claimed', 'submitted')
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_apply apply
      WHERE apply.workspace_id = snapshot.workspace_id AND apply.source_id = snapshot.source_id
        AND apply.snapshot_digest = snapshot.snapshot_digest
        AND apply.status IN ('pending', 'running')
  )
  AND NOT EXISTS (
      SELECT 1 FROM role_source_plan plan
      WHERE plan.workspace_id = snapshot.workspace_id AND plan.source_id = snapshot.source_id
        AND (plan.from_snapshot_digest = snapshot.snapshot_digest OR plan.to_snapshot_digest = snapshot.snapshot_digest)
        AND (
          plan.created_at >= now() - @plan_grace_period::interval
          OR (
            (SELECT approval.decision FROM role_source_plan_approval approval
             WHERE approval.workspace_id = plan.workspace_id AND approval.source_id = plan.source_id
               AND approval.plan_digest = plan.plan_digest
             ORDER BY approval.created_at DESC, approval.id DESC LIMIT 1) = 'approved'
            AND NOT EXISTS (
                SELECT 1 FROM role_source_apply apply
                WHERE apply.workspace_id = plan.workspace_id AND apply.source_id = plan.source_id
                  AND apply.plan_digest = plan.plan_digest AND apply.status = 'succeeded'
            )
          )
      )
  )
), uniquely_reclaimable_artifacts AS MATERIALIZED (
    SELECT artifact.digest, artifact.size_bytes
    FROM role_source_artifact artifact
    WHERE artifact.workspace_id = @workspace_id
      AND EXISTS (
          SELECT 1
          FROM role_source_snapshot_artifact edge
          JOIN eligible candidate
            ON candidate.workspace_id = edge.workspace_id
           AND candidate.source_id = edge.source_id
           AND candidate.snapshot_digest = edge.snapshot_digest
          WHERE edge.workspace_id = artifact.workspace_id
            AND edge.artifact_digest = artifact.digest
      )
      AND NOT EXISTS (
          SELECT 1
          FROM role_source_snapshot_artifact retained_edge
          WHERE retained_edge.workspace_id = artifact.workspace_id
            AND retained_edge.artifact_digest = artifact.digest
            AND NOT EXISTS (
                SELECT 1
                FROM eligible candidate
                WHERE candidate.workspace_id = retained_edge.workspace_id
                  AND candidate.source_id = retained_edge.source_id
                  AND candidate.snapshot_digest = retained_edge.snapshot_digest
            )
      )
)
SELECT snapshot_digest, created_at, estimated_bytes,
       count(*) OVER ()::bigint AS eligible_count,
       COALESCE(sum(estimated_bytes) OVER (), 0)::bigint AS total_estimated_bytes,
       (SELECT COALESCE(sum(size_bytes), 0)::bigint FROM uniquely_reclaimable_artifacts)
           AS uniquely_reclaimable_bytes
FROM eligible
ORDER BY created_at, snapshot_digest
LIMIT @result_limit;

-- name: QueueEligibleRoleSourceRetentionCandidates :many
WITH latest_policy AS MATERIALIZED (
    SELECT DISTINCT ON (policy.source_id) policy.*
    FROM role_source_retention_policy policy
    ORDER BY policy.source_id, policy.version DESC
), successful AS MATERIALIZED (
    SELECT apply.source_id, apply.snapshot_digest, max(apply.completed_at) AS applied_at
    FROM role_source_apply apply
    WHERE apply.status = 'succeeded'
    GROUP BY apply.source_id, apply.snapshot_digest
), successful_rank AS MATERIALIZED (
    SELECT source_id, snapshot_digest,
           row_number() OVER (PARTITION BY source_id ORDER BY applied_at DESC, snapshot_digest DESC) AS reserve_rank
    FROM successful
), eligible AS MATERIALIZED (
    SELECT snapshot.*, policy.version AS policy_version,
           COALESCE((SELECT sum(edge.size_bytes) FROM role_source_snapshot_artifact edge
                     WHERE edge.workspace_id = snapshot.workspace_id AND edge.source_id = snapshot.source_id
                       AND edge.snapshot_digest = snapshot.snapshot_digest), 0)::bigint AS estimated_bytes
    FROM role_source_snapshot snapshot
    JOIN role_source source ON source.id = snapshot.source_id AND source.workspace_id = snapshot.workspace_id
    JOIN latest_policy policy ON policy.source_id = snapshot.source_id AND policy.workspace_id = snapshot.workspace_id
    WHERE policy.enabled
      AND snapshot.created_at < now() - make_interval(days => policy.minimum_age_days)
      AND snapshot.snapshot_digest IS DISTINCT FROM source.current_snapshot_digest
      AND NOT EXISTS (
          SELECT 1 FROM successful_rank reserved
          WHERE reserved.source_id = snapshot.source_id AND reserved.snapshot_digest = snapshot.snapshot_digest
            AND reserved.reserve_rank <= policy.keep_successful_snapshots
      )
      AND NOT EXISTS (
          SELECT 1 FROM role_source_legal_hold hold
          WHERE hold.workspace_id = snapshot.workspace_id AND hold.source_id = snapshot.source_id
            AND (hold.scope = 'source' OR hold.snapshot_digest = snapshot.snapshot_digest)
            AND NOT EXISTS (SELECT 1 FROM role_source_legal_hold_release release WHERE release.hold_id = hold.id)
      )
      AND NOT EXISTS (SELECT 1 FROM role_source_task_pin pin WHERE pin.source_id = snapshot.source_id AND pin.snapshot_digest = snapshot.snapshot_digest)
      AND NOT EXISTS (SELECT 1 FROM role_source_object_mapping mapping WHERE mapping.source_id = snapshot.source_id AND mapping.last_snapshot_digest = snapshot.snapshot_digest)
      AND NOT EXISTS (SELECT 1 FROM role_source_secret_transfer transfer WHERE transfer.source_id = snapshot.source_id AND transfer.snapshot_digest = snapshot.snapshot_digest AND transfer.status IN ('pending', 'claimed', 'submitted'))
      AND NOT EXISTS (SELECT 1 FROM role_source_apply apply WHERE apply.source_id = snapshot.source_id AND apply.snapshot_digest = snapshot.snapshot_digest AND apply.status IN ('pending', 'running'))
      AND NOT EXISTS (
          SELECT 1 FROM role_source_plan plan
          WHERE plan.source_id = snapshot.source_id
            AND (plan.from_snapshot_digest = snapshot.snapshot_digest OR plan.to_snapshot_digest = snapshot.snapshot_digest)
            AND (
              plan.created_at >= now() - @plan_grace_period::interval
              OR (
                (SELECT approval.decision FROM role_source_plan_approval approval
                 WHERE approval.source_id = plan.source_id AND approval.plan_digest = plan.plan_digest
                 ORDER BY approval.created_at DESC, approval.id DESC LIMIT 1) = 'approved'
                AND NOT EXISTS (SELECT 1 FROM role_source_apply apply WHERE apply.source_id = plan.source_id AND apply.plan_digest = plan.plan_digest AND apply.status = 'succeeded')
              )
            )
      )
      AND NOT EXISTS (SELECT 1 FROM role_source_retention_candidate candidate WHERE candidate.source_id = snapshot.source_id AND candidate.snapshot_digest = snapshot.snapshot_digest)
    ORDER BY snapshot.created_at, snapshot.source_id, snapshot.snapshot_digest
    LIMIT @candidate_limit
)
INSERT INTO role_source_retention_candidate (
    id, workspace_id, source_id, snapshot_digest, policy_version,
    snapshot_created_at, estimated_bytes
)
SELECT gen_random_uuid(), workspace_id, source_id, snapshot_digest, policy_version, created_at, estimated_bytes
FROM eligible
ON CONFLICT (source_id, snapshot_digest) DO NOTHING
RETURNING *;

-- name: ClaimNextRoleSourceRetentionCandidate :one
WITH candidate AS MATERIALIZED (
    SELECT id
    FROM role_source_retention_candidate
    WHERE (state = 'pending' OR (state = 'claimed' AND lease_expires_at <= now()))
      AND next_attempt_at <= now()
    ORDER BY next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_retention_candidate retention
SET state = 'claimed', attempt = attempt + 1, lease_token = @lease_token,
    lease_expires_at = now() + @lease_duration::interval, result_code = NULL,
    updated_at = now()
FROM candidate
WHERE retention.id = candidate.id
RETURNING retention.*;

-- name: GetRoleSourceRetentionCandidateForUpdate :one
SELECT * FROM role_source_retention_candidate
WHERE id = @id AND state = 'claimed' AND lease_token = @lease_token
  AND lease_expires_at > now()
FOR UPDATE;

-- name: GetRoleSourceRetentionCandidate :one
SELECT * FROM role_source_retention_candidate
WHERE id = @id AND state = 'claimed' AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: GetRoleSourceSnapshotForUpdate :one
SELECT * FROM role_source_snapshot
WHERE workspace_id = @workspace_id AND source_id = @source_id
  AND snapshot_digest = @snapshot_digest
FOR UPDATE;

-- name: GetRoleSourceRetentionBlocker :one
WITH latest_policy AS MATERIALIZED (
    SELECT policy.* FROM role_source_retention_policy policy
    WHERE policy.workspace_id = @workspace_id AND policy.source_id = @source_id
    ORDER BY policy.version DESC LIMIT 1
), successful AS MATERIALIZED (
    SELECT apply.snapshot_digest, max(apply.completed_at) AS applied_at
    FROM role_source_apply apply
    WHERE apply.workspace_id = @workspace_id AND apply.source_id = @source_id AND apply.status = 'succeeded'
    GROUP BY apply.snapshot_digest
), successful_rank AS MATERIALIZED (
    SELECT snapshot_digest, row_number() OVER (ORDER BY applied_at DESC, snapshot_digest DESC) AS reserve_rank
    FROM successful
)
SELECT CASE
    WHEN NOT EXISTS (SELECT 1 FROM role_source_snapshot snapshot WHERE snapshot.workspace_id = @workspace_id AND snapshot.source_id = @source_id AND snapshot.snapshot_digest = @snapshot_digest) THEN 'snapshot_missing'
    WHEN NOT EXISTS (SELECT 1 FROM latest_policy WHERE enabled) THEN 'policy_disabled'
    WHEN EXISTS (SELECT 1 FROM role_source_snapshot snapshot, latest_policy policy WHERE snapshot.workspace_id = @workspace_id AND snapshot.source_id = @source_id AND snapshot.snapshot_digest = @snapshot_digest AND snapshot.created_at >= now() - make_interval(days => policy.minimum_age_days)) THEN 'policy_age'
    WHEN EXISTS (SELECT 1 FROM role_source source WHERE source.id = @source_id AND source.workspace_id = @workspace_id AND source.current_snapshot_digest = @snapshot_digest) THEN 'current_snapshot'
    WHEN EXISTS (SELECT 1 FROM role_source_legal_hold hold WHERE hold.workspace_id = @workspace_id AND hold.source_id = @source_id AND (hold.scope = 'source' OR hold.snapshot_digest = @snapshot_digest) AND NOT EXISTS (SELECT 1 FROM role_source_legal_hold_release release WHERE release.hold_id = hold.id)) THEN 'legal_hold'
    WHEN EXISTS (SELECT 1 FROM role_source_task_pin pin WHERE pin.workspace_id = @workspace_id AND pin.source_id = @source_id AND pin.snapshot_digest = @snapshot_digest) THEN 'task_pin'
    WHEN EXISTS (SELECT 1 FROM role_source_object_mapping mapping WHERE mapping.workspace_id = @workspace_id AND mapping.source_id = @source_id AND mapping.last_snapshot_digest = @snapshot_digest) THEN 'object_mapping'
    WHEN EXISTS (SELECT 1 FROM role_source_secret_transfer transfer WHERE transfer.workspace_id = @workspace_id AND transfer.source_id = @source_id AND transfer.snapshot_digest = @snapshot_digest AND transfer.status IN ('pending', 'claimed', 'submitted')) THEN 'active_transfer'
    WHEN EXISTS (SELECT 1 FROM role_source_apply apply WHERE apply.workspace_id = @workspace_id AND apply.source_id = @source_id AND apply.snapshot_digest = @snapshot_digest AND apply.status IN ('pending', 'running')) THEN 'active_apply'
    WHEN EXISTS (
        SELECT 1 FROM role_source_plan plan
        WHERE plan.workspace_id = @workspace_id AND plan.source_id = @source_id
          AND (plan.from_snapshot_digest = @snapshot_digest OR plan.to_snapshot_digest = @snapshot_digest)
          AND (plan.created_at >= now() - @plan_grace_period::interval OR (
              (SELECT approval.decision FROM role_source_plan_approval approval WHERE approval.workspace_id = plan.workspace_id AND approval.source_id = plan.source_id AND approval.plan_digest = plan.plan_digest ORDER BY approval.created_at DESC, approval.id DESC LIMIT 1) = 'approved'
              AND NOT EXISTS (SELECT 1 FROM role_source_apply apply WHERE apply.workspace_id = plan.workspace_id AND apply.source_id = plan.source_id AND apply.plan_digest = plan.plan_digest AND apply.status = 'succeeded')
          ))
    ) THEN 'recent_plan'
    WHEN EXISTS (SELECT 1 FROM successful_rank reserved, latest_policy policy WHERE reserved.snapshot_digest = @snapshot_digest AND reserved.reserve_rank <= policy.keep_successful_snapshots) THEN 'rollback_reserve'
    ELSE ''
END::text;

-- name: ReleaseRoleSourceRetentionCandidate :execrows
UPDATE role_source_retention_candidate
SET state = 'pending', lease_token = NULL, lease_expires_at = NULL,
    next_attempt_at = now() + @retry_delay::interval, result_code = @result_code,
    updated_at = now()
WHERE id = @id AND state = 'claimed' AND lease_token = @lease_token;

-- name: CompleteRoleSourceRetentionCandidate :execrows
UPDATE role_source_retention_candidate
SET state = 'completed', lease_token = NULL, lease_expires_at = NULL,
    result_code = 'pruned', completed_at = now(), updated_at = now()
WHERE id = @id AND state = 'claimed' AND lease_token = @lease_token;

-- name: CountRoleSourceRetentionCandidates :one
SELECT
    count(*) FILTER (WHERE state <> 'completed')::bigint AS active_count,
    COALESCE(extract(epoch FROM (now() - min(created_at) FILTER (WHERE state <> 'completed'))), 0)::bigint AS oldest_active_seconds
FROM role_source_retention_candidate;

-- name: DeleteRoleSourceSnapshotForRetention :execrows
DELETE FROM role_source_snapshot
WHERE workspace_id = @workspace_id AND source_id = @source_id
  AND snapshot_digest = @snapshot_digest;

-- name: DeleteUnreachableRoleSourceCapabilityVersions :execrows
DELETE FROM role_source_capability_version version
WHERE version.workspace_id = @workspace_id AND version.source_id = @source_id
  AND version.snapshot_digest = @snapshot_digest
  AND NOT EXISTS (
      SELECT 1
      FROM role_source_snapshot snapshot,
           jsonb_array_elements(snapshot.manifest->'capabilities') capability
      WHERE snapshot.workspace_id = version.workspace_id
        AND snapshot.source_id = version.source_id
        AND capability->>'id' = version.capability_id
        AND capability->>'version' = version.version
        AND capability = version.definition
  );

-- name: CountRoleSourcesByRuntime :one
-- Detached sources retain their last binding as audit context but no longer
-- keep that runtime alive. Rebind locks the new runtime before activation.
SELECT count(*) FROM role_source
WHERE runtime_id = @runtime_id AND state <> 'detached';

-- name: CountRoleSourcesByRuntimes :one
SELECT count(*) FROM role_source
WHERE runtime_id = ANY(@runtime_ids::uuid[]) AND state <> 'detached';

-- name: ReassignRoleSourcesToRuntime :execrows
UPDATE role_source
SET runtime_id = @new_runtime_id,
    version = version + 1,
    updated_at = now()
WHERE runtime_id = @old_runtime_id
  AND state <> 'detached';

-- name: RebindDetachedRoleSource :one
UPDATE role_source
SET runtime_id = @new_runtime_id,
    daemon_config_id = @daemon_config_id,
    config_redacted = @config_redacted,
    state = 'paused',
    updated_by = @actor_user_id,
    version = version + 1,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND version = @expected_version
  AND state = 'detached'
RETURNING *;

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
SELECT artifact.* FROM role_source_artifact artifact
JOIN role_source_artifact_integrity integrity
  ON integrity.workspace_id = artifact.workspace_id
 AND integrity.artifact_digest = artifact.digest
WHERE artifact.workspace_id = @workspace_id
  AND artifact.digest = ANY(@digests::text[])
  AND integrity.state IN ('pending', 'healthy')
ORDER BY artifact.digest;

-- name: ListRoleSourceArtifactsForApplyByDigests :many
-- Apply preloads verified bodies before its mutation transaction, then takes
-- short shared ledger locks in one batch so concurrent retention/GC cannot
-- remove or retarget those digests before commit.
SELECT artifact.* FROM role_source_artifact artifact
JOIN role_source_artifact_integrity integrity
  ON integrity.workspace_id = artifact.workspace_id
 AND integrity.artifact_digest = artifact.digest
WHERE artifact.workspace_id = @workspace_id
  AND artifact.digest = ANY(@digests::text[])
  AND integrity.state IN ('pending', 'healthy')
ORDER BY artifact.digest
FOR SHARE OF artifact, integrity;

-- name: ListRoleSourceArtifactsForSnapshotByDigests :many
-- Snapshot acceptance locks every ready body before it publishes reachability
-- edges. A collector uses FOR UPDATE SKIP LOCKED, so exactly one side wins:
-- either the snapshot commits its edge or the report observes the body absent.
SELECT artifact.* FROM role_source_artifact artifact
JOIN role_source_artifact_integrity integrity
  ON integrity.workspace_id = artifact.workspace_id
 AND integrity.artifact_digest = artifact.digest
WHERE artifact.workspace_id = @workspace_id
  AND artifact.digest = ANY(@digests::text[])
  AND integrity.state IN ('pending', 'healthy')
ORDER BY artifact.digest
FOR SHARE OF artifact, integrity;

-- name: MarkRoleSourceArtifactUploadedForIntegrity :one
INSERT INTO role_source_artifact_integrity (
    workspace_id, artifact_digest, storage_key, size_bytes, state, next_check_at
) VALUES (
    @workspace_id, @artifact_digest, @storage_key, @size_bytes, 'pending', now()
)
ON CONFLICT (workspace_id, artifact_digest) DO UPDATE
SET storage_key = EXCLUDED.storage_key,
    size_bytes = EXCLUDED.size_bytes,
    state = 'pending',
    last_outcome = CASE
        WHEN role_source_artifact_integrity.state = 'quarantined' THEN 'reuploaded'
        ELSE role_source_artifact_integrity.last_outcome
    END,
    lease_token = NULL,
    lease_expires_at = NULL,
    attempt = 0,
    repair_count = role_source_artifact_integrity.repair_count +
        CASE WHEN role_source_artifact_integrity.state = 'quarantined' THEN 1 ELSE 0 END,
    next_check_at = now(),
    updated_at = now()
RETURNING *;

-- name: ClaimNextRoleSourceArtifactIntegrity :one
WITH candidate AS MATERIALIZED (
    SELECT workspace_id, artifact_digest
    FROM role_source_artifact_integrity
    WHERE (state IN ('pending', 'healthy') AND next_check_at <= now())
       OR (state = 'checking' AND lease_expires_at < now())
    ORDER BY next_check_at, workspace_id, artifact_digest
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE role_source_artifact_integrity integrity
SET state = 'checking', lease_token = @lease_token,
    lease_expires_at = now() + @lease_duration::interval,
    attempt = attempt + 1, updated_at = now()
FROM candidate
WHERE integrity.workspace_id = candidate.workspace_id
  AND integrity.artifact_digest = candidate.artifact_digest
RETURNING integrity.*;

-- name: CompleteRoleSourceArtifactIntegrityHealthy :execrows
UPDATE role_source_artifact_integrity
SET state = 'healthy', last_outcome = 'healthy',
    lease_token = NULL, lease_expires_at = NULL, attempt = 0,
    check_count = check_count + 1,
    next_check_at = now() + @next_delay::interval,
    last_checked_at = now(), last_verified_at = now(), updated_at = now()
WHERE workspace_id = @workspace_id
  AND artifact_digest = @artifact_digest
  AND state = 'checking' AND lease_token = @lease_token;

-- name: QuarantineRoleSourceArtifactIntegrity :execrows
UPDATE role_source_artifact_integrity
SET state = 'quarantined', last_outcome = @outcome,
    lease_token = NULL, lease_expires_at = NULL,
    check_count = check_count + 1, failure_count = failure_count + 1,
    last_checked_at = now(), updated_at = now()
WHERE workspace_id = @workspace_id
  AND artifact_digest = @artifact_digest
  AND state = 'checking' AND lease_token = @lease_token;

-- name: ReleaseRoleSourceArtifactIntegrity :execrows
UPDATE role_source_artifact_integrity
SET state = 'pending', last_outcome = 'read_failed',
    lease_token = NULL, lease_expires_at = NULL,
    check_count = check_count + 1, failure_count = failure_count + 1,
    next_check_at = now() + @retry_delay::interval,
    last_checked_at = now(), updated_at = now()
WHERE workspace_id = @workspace_id
  AND artifact_digest = @artifact_digest
  AND state = 'checking' AND lease_token = @lease_token;

-- name: CountRoleSourceArtifactIntegrityStates :one
SELECT
    count(*) FILTER (WHERE state IN ('pending', 'checking'))::bigint AS pending_count,
    count(*) FILTER (WHERE state = 'quarantined')::bigint AS quarantined_count
FROM role_source_artifact_integrity;

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

-- name: LockRoleSourceArtifactsByDigestsForUpdate :many
-- Serialize prune accounting with snapshot publication (FOR SHARE) and the
-- artifact collector (FOR UPDATE). Every participant locks in digest order.
SELECT digest FROM role_source_artifact
WHERE workspace_id = @workspace_id
  AND digest = ANY(@artifact_digests::text[])
ORDER BY digest
FOR UPDATE;

-- name: SumUnreachableRoleSourceArtifactBytesByDigests :one
SELECT COALESCE(sum(artifact.size_bytes), 0)::bigint
FROM role_source_artifact artifact
WHERE artifact.workspace_id = @workspace_id
  AND artifact.digest = ANY(@artifact_digests::text[])
  AND NOT EXISTS (
      SELECT 1 FROM role_source_snapshot_artifact edge
      WHERE edge.workspace_id = artifact.workspace_id
        AND edge.artifact_digest = artifact.digest
  );

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
), removed_integrity AS (
    DELETE FROM role_source_artifact_integrity integrity
    USING queued
    WHERE integrity.storage_key = queued.storage_key
    RETURNING integrity.storage_key
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
    id, source_id, workspace_id, requested_by, expected_adapter_version, request_key_digest
) VALUES (
    @id, @source_id, @workspace_id, @requested_by, @expected_adapter_version, @request_key_digest
)
RETURNING *;

-- name: GetRoleSourceScanRequestByRequestKey :one
SELECT * FROM role_source_scan_request
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND request_key_digest = @request_key_digest;

-- name: GetRoleSourceScanRequest :one
SELECT * FROM role_source_scan_request
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id;

-- name: GetLatestRoleSourceScanRequest :one
SELECT * FROM role_source_scan_request
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY requested_at DESC, id
LIMIT 1;

-- name: ListRoleSourceScanRequests :many
-- Bounded operator history. Handler DTOs intentionally omit lease, runtime,
-- requester and idempotency evidence.
SELECT * FROM role_source_scan_request
WHERE source_id = @source_id AND workspace_id = @workspace_id
ORDER BY requested_at DESC, id DESC
LIMIT @result_limit;

-- name: GetRoleSourceScanRequestForUpdate :one
SELECT * FROM role_source_scan_request
WHERE id = @id AND source_id = @source_id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: CancelActiveRoleSourceScans :execrows
UPDATE role_source_scan_request
SET status = 'cancelled',
    error_code = @error_code,
    completed_at = now(),
    lease_expires_at = NULL
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND status IN ('queued', 'claimed');

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

-- name: CountRoleSourceMaterializationNameConflicts :one
-- Validate the entire requested target namespace once before per-object writes.
-- allowed_id is the exact mapped target that may already own the name. Any
-- other row is a conflict; this avoids LIMIT 1 nondeterminism when an existing
-- mapping and a user-owned object share a skill/autopilot name.
WITH requested AS (
    SELECT
        (item ->> 'target_kind')::TEXT AS target_kind,
        (item ->> 'name')::TEXT AS requested_name,
        NULLIF(item ->> 'allowed_id', '')::UUID AS allowed_id
    FROM jsonb_array_elements(@names::jsonb) AS item
)
SELECT count(*)::bigint
FROM requested
WHERE
    (target_kind = 'agent' AND EXISTS (
        SELECT 1 FROM agent target
        WHERE target.workspace_id = @workspace_id
          AND target.name = requested.requested_name
          AND (requested.allowed_id IS NULL OR target.id <> requested.allowed_id)
    ))
 OR (target_kind = 'skill' AND EXISTS (
        SELECT 1 FROM skill target
        WHERE target.workspace_id = @workspace_id
          AND target.name = requested.requested_name
          AND (requested.allowed_id IS NULL OR target.id <> requested.allowed_id)
    ))
 OR (target_kind = 'autopilot' AND EXISTS (
        SELECT 1 FROM autopilot target
        WHERE target.workspace_id = @workspace_id
          AND target.status <> 'archived'
          AND target.title = requested.requested_name
          AND (requested.allowed_id IS NULL OR target.id <> requested.allowed_id)
    ));

-- name: ListRoleSourceAdoptionTargetsForUpdate :many
-- Resolve same-name targets in one canonical, tenant-scoped query. During plan
-- creation target_id is empty and every matching row is returned so ambiguity
-- is explicit. During apply target_id is the immutable approved identity. The
-- selected rows are locked before their version commitment is revalidated.
-- managed_by_source_id is deliberately returned: a target already controlled
-- by any live role-source mapping can never be adopted by another source key.
WITH requested AS MATERIALIZED (
    SELECT
        item ->> 'target_kind' AS target_kind,
        item ->> 'name' AS requested_name,
        NULLIF(item ->> 'target_id', '')::UUID AS target_id
    FROM jsonb_array_elements(@targets::jsonb) AS item
), agent_targets AS (
    SELECT requested.target_kind, requested.requested_name, target.id AS target_id,
           target.updated_at, (target.kind = 'user' AND target.archived_at IS NULL) AS adoption_eligible,
           NULL::UUID AS dependency_target_id,
           (SELECT mapping.source_id
            FROM role_source_object_mapping mapping
            WHERE mapping.workspace_id = @workspace_id
              AND mapping.target_kind = 'agent'
              AND mapping.target_id = target.id
            ORDER BY mapping.source_id, mapping.source_kind, mapping.source_parent_id, mapping.source_object_id
            LIMIT 1) AS managed_by_source_id
    FROM requested
    JOIN agent target
      ON requested.target_kind = 'agent'
     AND target.workspace_id = @workspace_id
     AND target.name = requested.requested_name
     AND (requested.target_id IS NULL OR target.id = requested.target_id)
    FOR UPDATE OF target
), skill_targets AS (
    SELECT requested.target_kind, requested.requested_name, target.id AS target_id,
           target.updated_at, TRUE AS adoption_eligible, NULL::UUID AS dependency_target_id,
           (SELECT mapping.source_id
            FROM role_source_object_mapping mapping
            WHERE mapping.workspace_id = @workspace_id
              AND mapping.target_kind = 'skill'
              AND mapping.target_id = target.id
            ORDER BY mapping.source_id, mapping.source_kind, mapping.source_parent_id, mapping.source_object_id
            LIMIT 1) AS managed_by_source_id
    FROM requested
    JOIN skill target
      ON requested.target_kind = 'skill'
     AND target.workspace_id = @workspace_id
     AND target.name = requested.requested_name
     AND (requested.target_id IS NULL OR target.id = requested.target_id)
    FOR UPDATE OF target
), autopilot_targets AS (
    SELECT requested.target_kind, requested.requested_name, target.id AS target_id,
           target.updated_at, (target.assignee_type = 'agent') AS adoption_eligible,
           target.assignee_id AS dependency_target_id,
           (SELECT mapping.source_id
            FROM role_source_object_mapping mapping
            WHERE mapping.workspace_id = @workspace_id
              AND mapping.target_kind = 'autopilot'
              AND mapping.target_id = target.id
            ORDER BY mapping.source_id, mapping.source_kind, mapping.source_parent_id, mapping.source_object_id
            LIMIT 1) AS managed_by_source_id
    FROM requested
    JOIN autopilot target
      ON requested.target_kind = 'autopilot'
     AND target.workspace_id = @workspace_id
     AND target.status <> 'archived'
     AND target.title = requested.requested_name
     AND (requested.target_id IS NULL OR target.id = requested.target_id)
    FOR UPDATE OF target
), matches AS (
    SELECT * FROM agent_targets
    UNION ALL
    SELECT * FROM skill_targets
    UNION ALL
    SELECT * FROM autopilot_targets
)
SELECT target_kind::TEXT AS target_kind, requested_name::TEXT AS requested_name,
       target_id, updated_at, adoption_eligible, dependency_target_id, managed_by_source_id
FROM matches
ORDER BY target_kind, requested_name, target_id;

-- name: MaterializeRoleSourceAgents :many
-- Create or update one bounded role batch. The caller preallocates every new
-- ID and verifies that this statement returns the exact requested ID set.
-- Updates are deliberately limited to source-owned fields; permission, owner,
-- lifecycle, model, secrets, MCP and user preferences remain untouched.
WITH input AS MATERIALIZED (
    SELECT
        (item ->> 'id')::UUID AS id,
        item ->> 'operation' AS operation,
        item ->> 'name' AS name,
        item ->> 'description' AS description,
        item ->> 'runtime_mode' AS runtime_mode,
        (item ->> 'runtime_id')::UUID AS runtime_id,
        (item ->> 'owner_id')::UUID AS owner_id,
        item ->> 'instructions' AS instructions
    FROM jsonb_array_elements(@agents::jsonb) AS item
), updated AS (
    UPDATE agent target
    SET name = input.name,
        description = input.description,
        runtime_mode = input.runtime_mode,
        runtime_id = input.runtime_id,
        instructions = input.instructions,
        updated_at = now()
    FROM input
    WHERE input.operation = 'update'
      AND target.id = input.id
      AND target.workspace_id = @workspace_id
      AND target.kind = 'user'
      AND target.archived_at IS NULL
    RETURNING target.id
), inserted AS (
    INSERT INTO agent (
        id, workspace_id, name, description, runtime_mode, runtime_config,
        runtime_id, visibility, permission_mode, max_concurrent_tasks,
        owner_id, instructions, custom_env, custom_args, mcp_config
    )
    SELECT
        id, @workspace_id, name, description, runtime_mode, '{}'::jsonb,
        runtime_id, 'private', 'private', 1,
        owner_id, instructions, '{}'::jsonb, '[]'::jsonb, NULL
    FROM input
    WHERE operation = 'create'
    RETURNING agent.id
)
SELECT id FROM updated
UNION ALL
SELECT id FROM inserted
ORDER BY id;

-- name: UpsertRoleSourceObjectMappings :many
-- One apply can materialize thousands of objects. Send each caller-bounded
-- mapping batch with explicit JSON field extraction and database casts so the
-- transaction does not pay one network round trip per object. Row triggers
-- still execute for every affected mapping, and the caller verifies the exact
-- returned source-object set.
WITH input AS (
    SELECT
        item ->> 'source_kind' AS source_kind,
        item ->> 'source_parent_id' AS source_parent_id,
        item ->> 'source_object_id' AS source_object_id,
        item ->> 'target_kind' AS target_kind,
        (item ->> 'target_id')::UUID AS target_id,
        item -> 'ownership_mask' AS ownership_mask,
        item ->> 'last_applied_digest' AS last_applied_digest,
        item ->> 'last_snapshot_digest' AS last_snapshot_digest,
        (item ->> 'archived_at')::TIMESTAMPTZ AS archived_at
    FROM jsonb_array_elements(@mappings::jsonb) AS item
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

-- name: EnsureRoleSourceCapabilityVersions :many
-- Materialize bounded immutable capability-version batches. Existing rows are
-- accepted only when tenant, identity, digest and canonical JSON definition
-- match exactly. The preexisting CTE reads the statement snapshot; inserted
-- rows are returned only through RETURNING, avoiding double counts.
WITH requested AS MATERIALIZED (
    SELECT
        item ->> 'capability_id' AS capability_id,
        item ->> 'version' AS version,
        item ->> 'object_digest' AS object_digest,
        item -> 'definition' AS definition
    FROM jsonb_array_elements(@versions::jsonb) AS item
), preexisting AS MATERIALIZED (
    SELECT requested.capability_id, requested.version, requested.object_digest
    FROM requested
    JOIN role_source_capability_version existing
      ON existing.workspace_id = @workspace_id
     AND existing.source_id = @source_id
     AND existing.capability_id = requested.capability_id
     AND existing.version = requested.version
     AND existing.object_digest = requested.object_digest
     AND existing.definition = requested.definition
), inserted AS (
    INSERT INTO role_source_capability_version (
        workspace_id, source_id, capability_id, version, object_digest,
        definition, snapshot_digest
    )
    SELECT
        @workspace_id, @source_id, capability_id, version, object_digest,
        definition, @snapshot_digest
    FROM requested
    ON CONFLICT (source_id, capability_id, version, object_digest) DO NOTHING
    RETURNING capability_id, version, object_digest
)
SELECT capability_id, version, object_digest FROM inserted
UNION ALL
SELECT capability_id, version, object_digest FROM preexisting
ORDER BY 1, 2, 3;

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

-- name: ListRoleSourceSecretTransfersForPlan :many
SELECT DISTINCT ON (role_id) * FROM role_source_secret_transfer
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND plan_digest = @plan_digest
  AND approval_id = @approval_id
ORDER BY role_id, created_at DESC, id DESC
LIMIT @result_limit;

-- name: ClaimNextRoleSourceSecretTransfer :one
WITH source_candidate AS MATERIALIZED (
    SELECT source.id
    FROM role_source source
    WHERE source.runtime_id = @runtime_id
      AND source.state IN ('registered', 'active', 'error')
      AND EXISTS (
        SELECT 1 FROM role_source_secret_transfer pending
        WHERE pending.source_id = source.id
          AND pending.expires_at > now()
          AND (
            pending.status = 'pending'
            OR (pending.status = 'claimed' AND pending.lease_expires_at < now())
          )
      )
    ORDER BY source.id
    FOR UPDATE OF source SKIP LOCKED
    LIMIT 1
), candidate AS (
    SELECT transfer.id
    FROM role_source_secret_transfer transfer
    JOIN source_candidate source ON source.id = transfer.source_id
    WHERE transfer.runtime_id = @runtime_id
      AND transfer.expires_at > now()
      AND (
        transfer.status = 'pending'
        OR (transfer.status = 'claimed' AND transfer.lease_expires_at < now())
      )
    ORDER BY transfer.created_at, transfer.id
    FOR UPDATE OF transfer SKIP LOCKED
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

-- name: CancelActiveRoleSourceSecretTransfers :execrows
UPDATE role_source_secret_transfer
SET status = 'failed',
    envelope = NULL,
    private_key_ciphertext = decode(repeat('00', 60), 'hex'),
    lease_token = NULL,
    lease_expires_at = NULL,
    error_code = @error_code
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND status IN ('pending', 'claimed', 'submitted');

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

-- name: ListRoleSourceLifecycleAuditEvents :many
-- Safe bounded projection source for lifecycle history. The handler validates
-- each hash-chained event and returns only lifecycle fields.
SELECT * FROM role_source_audit_event
WHERE source_id = @source_id
  AND workspace_id = @workspace_id
  AND event_type IN ('source_paused', 'source_resumed', 'source_detached', 'source_rebound')
ORDER BY sequence DESC
LIMIT @result_limit;
