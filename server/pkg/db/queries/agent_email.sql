-- name: CreateAgentEmailPolicyVersion :one
INSERT INTO agent_email_policy_version (
    id, workspace_id, agent_id, version, config_digest, agent_prompt_digest,
    plugin_release_id, plugin_artifact_digest, plugin_entry_digest,
    interface_version, provider_route_id, provider_route_digest,
    sender_identity_id, from_address, recipient_policy, rate_policy,
    created_by, created_at
) VALUES (
    @id, @workspace_id, @agent_id, @version, @config_digest, @agent_prompt_digest,
    @plugin_release_id, @plugin_artifact_digest, @plugin_entry_digest,
    @interface_version, @provider_route_id, @provider_route_digest,
    @sender_identity_id, @from_address, @recipient_policy, @rate_policy,
    @created_by, @created_at
)
RETURNING *;

-- name: GetAgentEmailPolicyVersion :one
SELECT * FROM agent_email_policy_version
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND id = @id;

-- name: GetLatestAgentEmailPolicyVersionByDigest :one
SELECT * FROM agent_email_policy_version
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND config_digest = @config_digest
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: LockAgentEmailPolicyState :one
SELECT * FROM agent_email_policy_state
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
FOR UPDATE;

-- name: GetAgentEmailPolicyState :one
SELECT * FROM agent_email_policy_state
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id;

-- name: UpsertAgentEmailPolicyState :one
INSERT INTO agent_email_policy_state (
    workspace_id, agent_id, current_policy_version_id, current_config_digest,
    assigned_approver_user_id, approval_authority_digest, state, updated_at
) VALUES (
    @workspace_id, @agent_id, @current_policy_version_id, @current_config_digest,
    sqlc.narg('assigned_approver_user_id')::uuid,
    sqlc.narg('approval_authority_digest')::text,
    @state, now()
)
ON CONFLICT (workspace_id, agent_id) DO UPDATE SET
    current_policy_version_id = EXCLUDED.current_policy_version_id,
    current_config_digest = EXCLUDED.current_config_digest,
    assigned_approver_user_id = EXCLUDED.assigned_approver_user_id,
    approval_authority_digest = EXCLUDED.approval_authority_digest,
    state = EXCLUDED.state,
    updated_at = now()
RETURNING *;

-- name: UpdateAgentEmailPolicyStateDecision :one
UPDATE agent_email_policy_state
SET state = @state,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND current_policy_version_id = @current_policy_version_id
  AND current_config_digest = @current_config_digest
  AND approval_authority_digest = @approval_authority_digest
RETURNING *;

-- name: AppendAgentEmailApproval :one
INSERT INTO agent_email_approval (
    id, workspace_id, agent_id, policy_version_id, config_digest,
    approval_authority_digest, decision, request_key_digest,
    actor_user_id, reason_code, created_at
) VALUES (
    @id, @workspace_id, @agent_id, @policy_version_id, @config_digest,
    @approval_authority_digest, @decision, @request_key_digest,
    sqlc.narg('actor_user_id')::uuid, sqlc.narg('reason_code')::text,
    @created_at
)
RETURNING *;

-- name: GetAgentEmailApproval :one
SELECT * FROM agent_email_approval
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND id = @id;

-- name: GetLatestAgentEmailApproval :one
SELECT * FROM agent_email_approval
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND policy_version_id = @policy_version_id
  AND approval_authority_digest = @approval_authority_digest
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListAgentEmailApprovals :many
SELECT * FROM agent_email_approval
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
ORDER BY created_at DESC, id DESC
LIMIT @result_limit OFFSET @result_offset;

-- name: GetAgentEmailTaskAuthorizationSnapshot :one
SELECT
    task.id,
    task.agent_id,
    task.status,
    task.email_agent_config_digest,
    task.email_agent_policy_version_id,
    task.email_agent_approval_id
FROM agent_task_queue AS task
JOIN agent ON agent.id = task.agent_id
WHERE task.id = @task_id
  AND task.agent_id = @agent_id
  AND agent.workspace_id = @workspace_id;

-- name: GetApprovedAgentEmailPolicyForAdmission :one
SELECT
    policy.id AS policy_version_id,
    policy.config_digest,
    policy.agent_prompt_digest,
    policy.plugin_release_id,
    policy.plugin_artifact_digest,
    policy.plugin_entry_digest,
    policy.interface_version,
    policy.provider_route_id,
    policy.provider_route_digest,
    policy.sender_identity_id,
    policy.from_address,
    policy.recipient_policy,
    policy.rate_policy,
    approval.id AS approval_id,
    approval.actor_user_id AS approved_by,
    approval.created_at AS approved_at
FROM agent_task_queue AS task
JOIN agent ON agent.id = task.agent_id
          AND agent.workspace_id = @workspace_id
          AND agent.archived_at IS NULL
JOIN agent_email_policy_state AS state
  ON state.workspace_id = @workspace_id AND state.agent_id = @agent_id
JOIN agent_email_policy_version AS policy
  ON policy.workspace_id = @workspace_id
 AND policy.agent_id = @agent_id
 AND policy.id = state.current_policy_version_id
JOIN LATERAL (
    SELECT decision.*
    FROM agent_email_approval AS decision
    WHERE decision.workspace_id = @workspace_id
      AND decision.agent_id = @agent_id
      AND decision.policy_version_id = policy.id
      AND decision.config_digest = policy.config_digest
      AND decision.approval_authority_digest = @approval_authority_digest
    ORDER BY decision.created_at DESC, decision.id DESC
    LIMIT 1
) AS approval ON approval.decision = 'approved'
JOIN member AS approver_member
  ON approver_member.workspace_id = @workspace_id
 AND approver_member.user_id = state.assigned_approver_user_id
 AND approver_member.role IN ('owner', 'admin')
WHERE task.id = @task_id
  AND task.agent_id = @agent_id
  AND task.status = 'running'
  AND @production_enabled::boolean
  AND @cohort_allowed::boolean
  AND task.email_agent_config_digest = policy.config_digest
  AND task.email_agent_policy_version_id = policy.id
  AND task.email_agent_approval_id = approval.id
  AND state.current_config_digest = policy.config_digest
  AND state.approval_authority_digest = @approval_authority_digest
  AND approval.actor_user_id = state.assigned_approver_user_id
  AND state.state = 'approved';

-- name: CreateAgentEmailMessage :one
INSERT INTO email_message (
    id, workspace_id, agent_id, task_id, mode, policy_version_id, approval_id,
    idempotency_key_digest, request_digest, provider_route_id,
    sender_identity_id, recipient_count, intended_to, effective_to,
    subject_digest, body_digest, encrypted_payload_ref, next_attempt_at,
    created_at, updated_at
) VALUES (
    @id, @workspace_id, @agent_id, @task_id, @mode, @policy_version_id,
    sqlc.narg('approval_id')::uuid, @idempotency_key_digest, @request_digest,
    @provider_route_id, @sender_identity_id, @recipient_count,
    @intended_to, @effective_to, @subject_digest, @body_digest,
    @encrypted_payload_ref, @next_attempt_at, @created_at, @created_at
)
RETURNING *;

-- name: GetAgentEmailMessageByIdempotencyKey :one
SELECT * FROM email_message
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND idempotency_key_digest = @idempotency_key_digest;

-- name: GetAgentEmailMessageForTask :one
SELECT * FROM email_message
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND task_id = @task_id
  AND id = @id;

-- name: ReserveAgentEmailQuota :one
SELECT reserve_agent_email_quota(
    @workspace_id, @message_id, @agent_id, @provider_route_id,
    @sender_identity_id, @recipient_count,
    @provider_per_minute, @provider_per_day,
    @workspace_per_minute, @workspace_per_day,
    @agent_per_minute, @agent_per_day,
    @sender_per_minute, @sender_per_day
) AS reserved;

-- name: CreateEmailDeliveryRecipient :one
INSERT INTO email_delivery_recipient (
    id, workspace_id, message_id, recipient_digest, encrypted_recipient_ref,
    created_at, updated_at
) VALUES (
    @id, @workspace_id, @message_id, @recipient_digest,
    @encrypted_recipient_ref, @created_at, @created_at
)
RETURNING *;

-- name: ListEmailDeliveryRecipients :many
SELECT * FROM email_delivery_recipient
WHERE workspace_id = @workspace_id
  AND message_id = @message_id
ORDER BY created_at, id;

-- name: UpdateEmailDeliveryRecipientStatus :one
UPDATE email_delivery_recipient AS recipient
SET status = @status,
    provider_recipient_id = sqlc.narg('provider_recipient_id')::text,
    last_error_code = sqlc.narg('last_error_code')::text,
    completed_at = sqlc.narg('completed_at')::timestamptz,
    updated_at = now()
FROM email_message AS message
WHERE recipient.workspace_id = @workspace_id
  AND recipient.message_id = @message_id
  AND recipient.id = @id
  AND message.workspace_id = @workspace_id
  AND message.id = recipient.message_id
  AND message.status = 'sending'
  AND message.lease_token = @lease_token
  AND message.lease_generation = @lease_generation
  AND message.authorized_at IS NOT NULL
  AND message.authorized_lease_generation = message.lease_generation
RETURNING recipient.*;

-- name: ClaimAgentEmailMessages :many
WITH candidate AS (
    SELECT id
    FROM email_message
    WHERE workspace_id = @workspace_id
      AND status = 'queued'
      AND attempt_count < 20
      AND next_attempt_at <= now()
    ORDER BY next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT @claim_limit
)
UPDATE email_message AS message
SET status = 'sending',
    attempt_count = message.attempt_count + 1,
    lease_token = @lease_token,
    lease_generation = message.lease_generation + 1,
    lease_expires_at = now() + @lease_duration::interval,
    authorized_at = NULL,
    authorized_lease_generation = NULL,
    last_error_code = NULL,
    updated_at = now()
FROM candidate
WHERE message.workspace_id = @workspace_id
  AND message.id = candidate.id
RETURNING message.*;

-- name: ReconcileExpiredAgentEmailMessageLeases :execrows
UPDATE email_message
SET status = CASE
        WHEN authorized_at IS NOT NULL THEN 'ambiguous'
        WHEN attempt_count >= 20 THEN 'dead'
        ELSE 'queued'
    END,
    next_attempt_at = CASE
        WHEN authorized_at IS NULL AND attempt_count < 20 THEN now()
        ELSE next_attempt_at
    END,
    last_error_code = CASE
        WHEN authorized_at IS NOT NULL THEN 'lease_expired_after_authorization'
        WHEN attempt_count >= 20 THEN COALESCE(last_error_code, 'lease_expired_after_max_attempts')
        ELSE COALESCE(last_error_code, 'lease_expired_before_authorization')
    END,
    completed_at = CASE
        WHEN authorized_at IS NOT NULL OR attempt_count >= 20 THEN now()
        ELSE NULL
    END,
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND status = 'sending'
  AND lease_expires_at <= now();

-- name: AuthorizeClaimedAgentEmailMessage :one
WITH locked_policy_state AS MATERIALIZED (
    SELECT state.*
    FROM agent_email_policy_state AS state
    JOIN email_message AS candidate
      ON candidate.workspace_id = @workspace_id
     AND candidate.agent_id = state.agent_id
     AND candidate.id = @id
     AND candidate.mode = 'production'
    WHERE state.workspace_id = @workspace_id
    FOR UPDATE OF state
)
UPDATE email_message AS message
SET authorized_at = now(),
    authorized_lease_generation = message.lease_generation,
    updated_at = now()
FROM agent_task_queue AS task
JOIN agent AS task_agent
  ON task_agent.id = task.agent_id
 AND task_agent.workspace_id = @workspace_id
 AND task_agent.archived_at IS NULL
JOIN agent_email_policy_version AS pinned_policy
  ON pinned_policy.workspace_id = @workspace_id
 AND pinned_policy.agent_id = task.agent_id
 AND pinned_policy.id = task.email_agent_policy_version_id
 AND pinned_policy.config_digest = task.email_agent_config_digest
LEFT JOIN "user" AS owner_user
  ON owner_user.id = task_agent.owner_id
LEFT JOIN member AS owner_member
  ON owner_member.workspace_id = @workspace_id
 AND owner_member.user_id = task_agent.owner_id
WHERE message.workspace_id = @workspace_id
  AND message.id = @id
  AND task.id = message.task_id
  AND task.agent_id = message.agent_id
  AND task.status = 'running'
  AND message.policy_version_id = task.email_agent_policy_version_id
  AND message.provider_route_id = pinned_policy.provider_route_id
  AND message.sender_identity_id = pinned_policy.sender_identity_id
  AND message.recipient_count = jsonb_array_length(message.effective_to)
  AND message.status = 'sending'
  AND message.lease_token = @lease_token
  AND message.lease_generation = @lease_generation
  AND (
      (
          message.mode = 'sandbox'
          AND message.approval_id IS NULL
          AND message.recipient_count = 1
          AND owner_member.user_id IS NOT NULL
          AND owner_user.email = @sandbox_owner_email
          AND message.effective_to = jsonb_build_array(@sandbox_owner_email::text)
      )
      OR (
          message.mode = 'production'
          AND @production_enabled::boolean
          AND @cohort_allowed::boolean
          AND message.approval_id = task.email_agent_approval_id
          AND NOT EXISTS (
              SELECT 1
              FROM jsonb_array_elements_text(message.effective_to) AS recipient(address)
              WHERE NOT (pinned_policy.recipient_policy->'allowlist' ? recipient.address)
          )
          AND EXISTS (
              SELECT 1
              FROM locked_policy_state AS state
              JOIN LATERAL (
                  SELECT decision.id, decision.decision, decision.actor_user_id
                  FROM agent_email_approval AS decision
                  WHERE decision.workspace_id = @workspace_id
                    AND decision.agent_id = message.agent_id
                    AND decision.policy_version_id = pinned_policy.id
                    AND decision.config_digest = pinned_policy.config_digest
                    AND decision.approval_authority_digest = @approval_authority_digest
                  ORDER BY decision.created_at DESC, decision.id DESC
                  LIMIT 1
              ) AS approval ON approval.decision = 'approved'
              JOIN member AS approver_member
                ON approver_member.workspace_id = @workspace_id
               AND approver_member.user_id = state.assigned_approver_user_id
               AND approver_member.role IN ('owner', 'admin')
              WHERE state.workspace_id = @workspace_id
                AND state.agent_id = message.agent_id
                AND state.state = 'approved'
                AND state.current_policy_version_id = pinned_policy.id
                AND state.current_config_digest = pinned_policy.config_digest
                AND state.approval_authority_digest = @approval_authority_digest
                AND approval.id = message.approval_id
                AND approval.id = task.email_agent_approval_id
                AND approval.actor_user_id = state.assigned_approver_user_id
          )
      )
  )
RETURNING message.*;

-- name: RetryClaimedAgentEmailMessage :one
UPDATE email_message
SET status = 'queued',
    next_attempt_at = @next_attempt_at,
    last_error_code = @last_error_code,
    lease_token = NULL,
    lease_expires_at = NULL,
    authorized_at = NULL,
    authorized_lease_generation = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND status = 'sending'
  AND lease_token = @lease_token
  AND lease_generation = @lease_generation
  AND authorized_at IS NOT NULL
  AND authorized_lease_generation = lease_generation
RETURNING *;

-- name: AcceptClaimedAgentEmailMessage :one
UPDATE email_message
SET status = 'accepted',
    provider_message_id = @provider_message_id,
    provider_status = @provider_status,
    accepted_at = now(),
    completed_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND status = 'sending'
  AND lease_token = @lease_token
  AND lease_generation = @lease_generation
  AND authorized_at IS NOT NULL
  AND authorized_lease_generation = lease_generation
RETURNING *;

-- name: FailClaimedAgentEmailMessagePermanent :one
UPDATE email_message
SET status = 'failed_permanent',
    provider_status = sqlc.narg('provider_status')::text,
    last_error_code = @last_error_code,
    completed_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND status = 'sending'
  AND lease_token = @lease_token
  AND lease_generation = @lease_generation
  AND authorized_at IS NOT NULL
  AND authorized_lease_generation = lease_generation
RETURNING *;

-- name: MarkClaimedAgentEmailMessageAmbiguous :one
UPDATE email_message
SET status = 'ambiguous',
    provider_message_id = sqlc.narg('provider_message_id')::text,
    provider_status = sqlc.narg('provider_status')::text,
    last_error_code = @last_error_code,
    completed_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND status = 'sending'
  AND lease_token = @lease_token
  AND lease_generation = @lease_generation
  AND authorized_at IS NOT NULL
  AND authorized_lease_generation = lease_generation
RETURNING *;

-- name: CancelClaimedAgentEmailMessage :one
UPDATE email_message
SET status = 'cancelled',
    last_error_code = @last_error_code,
    completed_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
  AND status = 'sending'
  AND lease_token = @lease_token
  AND lease_generation = @lease_generation
RETURNING *;

-- name: DeleteExpiredAgentEmailQuotaReservations :execrows
DELETE FROM agent_email_quota_reservation
WHERE workspace_id = @workspace_id
  AND created_at < @before_time;
