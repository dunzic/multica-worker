-- Connector-neutral, content-free outbound delivery ledger. Message bodies and
-- attachment contents remain in their existing authorized stores; this table
-- keeps only routing identities, content digests, provider message ids and
-- tamper-evident evidence required for delivery audit and retry fencing.
--
-- No foreign keys: installation/task/session ownership is enforced by the
-- application, matching the channel_* schema policy.
CREATE TABLE channel_delivery (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    installation_id UUID,
    task_id UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    channel_type TEXT NOT NULL CHECK (channel_type ~ '^[a-z][a-z0-9_]{1,63}$'),
    channel_chat_id TEXT NOT NULL CHECK (char_length(channel_chat_id) BETWEEN 1 AND 512),
    operation_kind TEXT NOT NULL CHECK (operation_kind IN ('chat_reply', 'failure_notice')),
    correlation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    payload_digest TEXT NOT NULL CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'readback', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 1 CHECK (attempt_count BETWEEN 1 AND 100),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    external_message_id TEXT CHECK (external_message_id IS NULL OR char_length(external_message_id) BETWEEN 1 AND 512),
    evidence JSONB CHECK (evidence IS NULL OR jsonb_typeof(evidence) = 'object'),
    evidence_digest TEXT CHECK (evidence_digest IS NULL OR evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    last_error_code TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 100),
    delivered_at TIMESTAMPTZ,
    readback_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (status = 'pending' AND installation_id IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR status <> 'pending'
    ),
    CHECK (
        (status IN ('delivered', 'readback') AND external_message_id IS NOT NULL AND evidence IS NOT NULL
            AND evidence_digest IS NOT NULL AND delivered_at IS NOT NULL)
        OR status NOT IN ('delivered', 'readback')
    ),
    CHECK ((status = 'readback' AND readback_at IS NOT NULL) OR status <> 'readback'),
    CHECK ((status = 'failed' AND failed_at IS NOT NULL) OR status <> 'failed')
);
