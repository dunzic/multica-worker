-- name: ClaimChannelDelivery :one
INSERT INTO channel_delivery (
    workspace_id, installation_id, task_id, chat_session_id, channel_type,
    channel_chat_id, operation_kind, payload_digest, lease_token, lease_expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, gen_random_uuid(), now() + INTERVAL '30 seconds'
)
ON CONFLICT (installation_id, task_id, operation_kind) WHERE installation_id IS NOT NULL DO UPDATE
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + INTERVAL '30 seconds',
    status = 'pending',
    attempt_count = channel_delivery.attempt_count + 1,
    last_error_code = NULL,
    failed_at = NULL,
    ambiguous_at = NULL,
    external_message_id = NULL,
    evidence = NULL,
    evidence_digest = NULL,
    retry_publish_token = NULL,
    retry_publish_expires_at = NULL,
    updated_at = now()
WHERE channel_delivery.payload_digest = EXCLUDED.payload_digest
  AND channel_delivery.attempt_count < 100
  AND (
      channel_delivery.status = 'failed'
      OR (
          channel_delivery.status = 'retry_authorized'
          AND EXISTS (
              SELECT 1
              FROM channel_delivery_reconciliation reconciliation
              WHERE reconciliation.delivery_id = channel_delivery.id
                AND reconciliation.generation = channel_delivery.reconciliation_count
                AND reconciliation.outcome = 'confirmed_not_delivered'
                AND reconciliation.expected_ambiguity_evidence_digest = channel_delivery.evidence_digest
          )
      )
  )
RETURNING *;

-- name: GetChannelDeliveryByIdentity :one
SELECT * FROM channel_delivery
WHERE installation_id = $1 AND task_id = $2 AND operation_kind = $3;

-- name: CompleteChannelDelivery :one
UPDATE channel_delivery
SET status = 'delivered',
    external_message_id = $3,
    evidence = $4,
    evidence_digest = $5,
    delivered_at = $6,
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'pending'
RETURNING *;

-- name: FailChannelDelivery :one
UPDATE channel_delivery
SET status = 'failed',
    last_error_code = $3,
    failed_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'pending'
RETURNING *;

-- name: MarkChannelDeliveryAmbiguous :one
UPDATE channel_delivery
SET status = 'ambiguous',
    external_message_id = sqlc.narg('external_message_id')::text,
    evidence = @evidence,
    evidence_digest = @evidence_digest,
    last_error_code = @last_error_code,
    ambiguous_at = @ambiguous_at,
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = @id AND lease_token = @lease_token AND status = 'pending'
RETURNING *;

-- name: GetChannelDeliveryByExternalMessage :one
SELECT * FROM channel_delivery
WHERE installation_id = $1 AND external_message_id = $2;

-- name: MarkChannelDeliveryReadback :one
UPDATE channel_delivery
SET status = 'readback',
    evidence = $3,
    evidence_digest = $4,
    readback_at = $5,
    updated_at = now()
WHERE id = $1 AND status = 'delivered' AND external_message_id = $2
RETURNING *;

-- name: ListChannelDeliveriesByWorkspace :many
SELECT * FROM channel_delivery
WHERE workspace_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: GetChannelDeliveryByID :one
SELECT * FROM channel_delivery WHERE id = $1;

-- name: GetChannelDeliveryForReconciliation :one
SELECT * FROM channel_delivery
WHERE id = $1
FOR UPDATE;

-- name: GetLatestChannelDeliveryReconciliation :one
SELECT * FROM channel_delivery_reconciliation
WHERE delivery_id = $1
ORDER BY generation DESC
LIMIT 1;

-- name: ListChannelDeliveryReconciliationsByDelivery :many
SELECT * FROM channel_delivery_reconciliation
WHERE delivery_id = $1
ORDER BY generation;

-- name: GetChannelDeliveryReconciliationByAuthorization :one
SELECT * FROM channel_delivery_reconciliation
WHERE authorization_id = $1;

-- name: ListLatestChannelDeliveryReconciliationsByWorkspace :many
SELECT DISTINCT ON (delivery_id) *
FROM channel_delivery_reconciliation
WHERE workspace_id = $1
ORDER BY delivery_id, generation DESC;

-- name: ListChannelDeliveryReconciliationsByWorkspaceDeliveries :many
SELECT * FROM channel_delivery_reconciliation
WHERE workspace_id = @workspace_id
  AND delivery_id = ANY(@delivery_ids::uuid[])
ORDER BY delivery_id, generation;

-- name: InsertChannelDeliveryReconciliation :one
INSERT INTO channel_delivery_reconciliation (
    id, delivery_id, workspace_id, authorization_id, generation,
    outcome, reason_code, external_evidence_digest,
    expected_ambiguity_evidence_digest, ambiguity_evidence,
    requester_key_id, approver_key_id, authorization_digest,
    requester_signature_digest, approver_signature_digest,
    previous_reconciliation_digest, reconciliation_digest, created_at
) VALUES (
    @id, @delivery_id, @workspace_id, @authorization_id, @generation,
    @outcome, @reason_code, @external_evidence_digest,
    @expected_ambiguity_evidence_digest, @ambiguity_evidence,
    @requester_key_id, @approver_key_id, @authorization_digest,
    @requester_signature_digest, @approver_signature_digest,
    sqlc.narg('previous_reconciliation_digest')::text,
    @reconciliation_digest, @created_at
)
RETURNING *;

-- name: ResolveChannelDeliveryAmbiguity :one
UPDATE channel_delivery
SET status = CASE
        WHEN @outcome::text = 'confirmed_not_delivered' THEN 'retry_authorized'
        ELSE 'reconciled'
    END,
    reconciliation_count = reconciliation_count + 1,
    last_reconciled_at = @reconciled_at,
    retry_publish_token = NULL,
    retry_publish_expires_at = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = @id
  AND status = 'ambiguous'
  AND reconciliation_count = @expected_reconciliation_count
  AND evidence_digest = @expected_ambiguity_evidence_digest
RETURNING *;

-- name: ClaimAuthorizedChannelDeliveryRetries :many
WITH due AS (
    SELECT id
    FROM channel_delivery
    WHERE status = 'retry_authorized'
      AND (retry_publish_expires_at IS NULL OR retry_publish_expires_at < now())
    ORDER BY last_reconciled_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE channel_delivery
SET retry_publish_token = gen_random_uuid(),
    retry_publish_expires_at = now() + INTERVAL '30 seconds',
    updated_at = now()
WHERE id IN (SELECT id FROM due)
RETURNING channel_delivery.*;

-- name: ClaimExpiredChannelDeliveryLeases :many
WITH due AS (
    SELECT id FROM channel_delivery
    WHERE status = 'pending' AND lease_expires_at < now()
    ORDER BY lease_expires_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE channel_delivery
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + INTERVAL '30 seconds',
    updated_at = now()
WHERE id IN (SELECT id FROM due)
RETURNING channel_delivery.*;

-- name: DeleteChannelDeliveriesByWorkspace :exec
DELETE FROM channel_delivery WHERE workspace_id = $1;
