-- A frozen provider-acceptance ambiguity may only leave quarantine through a
-- signed, two-person reconciliation receipt. The delivery row is mutable
-- operational state; channel_delivery_reconciliation is the append-only
-- evidence chain that preserves every ambiguity generation.
ALTER TABLE channel_delivery
    DROP CONSTRAINT channel_delivery_status_check,
    ADD COLUMN reconciliation_count SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN last_reconciled_at TIMESTAMPTZ,
    ADD COLUMN retry_publish_token UUID,
    ADD COLUMN retry_publish_expires_at TIMESTAMPTZ,
    ADD CONSTRAINT channel_delivery_status_check
        CHECK (status IN ('pending', 'delivered', 'readback', 'failed', 'ambiguous', 'retry_authorized', 'reconciled')) NOT VALID,
    ADD CONSTRAINT channel_delivery_reconciliation_count_check
        CHECK (reconciliation_count BETWEEN 0 AND 3) NOT VALID,
    ADD CONSTRAINT channel_delivery_reconciliation_state_check CHECK (
        (status IN ('retry_authorized', 'reconciled')
            AND reconciliation_count BETWEEN 1 AND 3
            AND last_reconciled_at IS NOT NULL
            AND lease_token IS NULL
            AND lease_expires_at IS NULL)
        OR status NOT IN ('retry_authorized', 'reconciled')
    ) NOT VALID,
    ADD CONSTRAINT channel_delivery_retry_publish_lease_check CHECK (
        (retry_publish_token IS NULL) = (retry_publish_expires_at IS NULL)
        AND (retry_publish_token IS NULL OR status = 'retry_authorized')
    ) NOT VALID;

CREATE TABLE channel_delivery_reconciliation (
    id UUID NOT NULL,
    delivery_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    authorization_id UUID NOT NULL,
    generation SMALLINT NOT NULL CHECK (generation BETWEEN 1 AND 3),
    outcome TEXT NOT NULL CHECK (outcome IN ('confirmed_delivered', 'confirmed_not_delivered', 'closed_no_retry')),
    reason_code TEXT NOT NULL CHECK (reason_code IN (
        'provider_delivery_confirmed',
        'provider_non_delivery_confirmed',
        'business_superseded',
        'risk_accepted'
    )),
    external_evidence_digest TEXT NOT NULL CHECK (external_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_ambiguity_evidence_digest TEXT NOT NULL CHECK (expected_ambiguity_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    ambiguity_evidence JSONB NOT NULL CHECK (jsonb_typeof(ambiguity_evidence) = 'object'),
    requester_key_id TEXT NOT NULL CHECK (requester_key_id ~ '^[a-z][a-z0-9_-]{1,63}$'),
    approver_key_id TEXT NOT NULL CHECK (approver_key_id ~ '^[a-z][a-z0-9_-]{1,63}$'),
    authorization_digest TEXT NOT NULL CHECK (authorization_digest ~ '^sha256:[0-9a-f]{64}$'),
    requester_signature_digest TEXT NOT NULL CHECK (requester_signature_digest ~ '^sha256:[0-9a-f]{64}$'),
    approver_signature_digest TEXT NOT NULL CHECK (approver_signature_digest ~ '^sha256:[0-9a-f]{64}$'),
    previous_reconciliation_digest TEXT CHECK (
        previous_reconciliation_digest IS NULL
        OR previous_reconciliation_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    reconciliation_digest TEXT NOT NULL CHECK (reconciliation_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (requester_key_id <> approver_key_id),
    CHECK ((generation = 1) = (previous_reconciliation_digest IS NULL)),
    CHECK (NOT (
        outcome = 'confirmed_not_delivered'
        AND ambiguity_evidence ->> 'ambiguity_reason' = 'partial_delivery'
    )),
    CHECK (
        (outcome = 'confirmed_delivered' AND reason_code = 'provider_delivery_confirmed')
        OR (outcome = 'confirmed_not_delivered' AND reason_code = 'provider_non_delivery_confirmed')
        OR (outcome = 'closed_no_retry' AND reason_code IN ('business_superseded', 'risk_accepted'))
    )
);
