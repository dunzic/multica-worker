-- Agent notification-email control plane and durable delivery ledger.
-- Relationships are application-owned: every tenant row carries workspace_id
-- and no foreign keys or cascading actions are used.

ALTER TABLE agent_task_queue
    ADD COLUMN email_agent_config_digest TEXT
        CHECK (email_agent_config_digest IS NULL OR email_agent_config_digest ~ '^sha256:[0-9a-f]{64}$');

CREATE TABLE agent_email_policy_version (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    config_digest TEXT NOT NULL CHECK (config_digest ~ '^sha256:[0-9a-f]{64}$'),
    agent_prompt_digest TEXT NOT NULL CHECK (agent_prompt_digest ~ '^sha256:[0-9a-f]{64}$'),
    plugin_release_id UUID NOT NULL,
    plugin_artifact_digest TEXT NOT NULL CHECK (plugin_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    plugin_entry_digest TEXT NOT NULL CHECK (plugin_entry_digest ~ '^sha256:[0-9a-f]{64}$'),
    interface_version TEXT NOT NULL CHECK (char_length(interface_version) BETWEEN 1 AND 128),
    provider_route_id UUID NOT NULL,
    provider_route_digest TEXT NOT NULL CHECK (provider_route_digest ~ '^sha256:[0-9a-f]{64}$'),
    sender_identity_id UUID NOT NULL,
    from_address TEXT NOT NULL CHECK (char_length(from_address) BETWEEN 3 AND 320),
    recipient_policy JSONB NOT NULL CHECK (jsonb_typeof(recipient_policy) = 'object'),
    rate_policy JSONB NOT NULL CHECK (jsonb_typeof(rate_policy) = 'object'),
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_email_policy_state (
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    current_policy_version_id UUID NOT NULL,
    current_config_digest TEXT NOT NULL CHECK (current_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    assigned_approver_user_id UUID,
    approval_authority_digest TEXT CHECK (
        approval_authority_digest IS NULL OR approval_authority_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    state TEXT NOT NULL CHECK (state IN ('draft', 'pending', 'approved', 'revoked', 'disabled')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((assigned_approver_user_id IS NULL) = (approval_authority_digest IS NULL))
);

CREATE TABLE agent_email_approval (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    policy_version_id UUID NOT NULL,
    config_digest TEXT NOT NULL CHECK (config_digest ~ '^sha256:[0-9a-f]{64}$'),
    approval_authority_digest TEXT NOT NULL CHECK (approval_authority_digest ~ '^sha256:[0-9a-f]{64}$'),
    decision TEXT NOT NULL CHECK (decision IN ('requested', 'approved', 'rejected', 'revoked', 'superseded')),
    request_key_digest TEXT NOT NULL CHECK (request_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    actor_user_id UUID,
    reason_code TEXT CHECK (reason_code IS NULL OR char_length(reason_code) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (decision NOT IN ('approved', 'rejected', 'revoked') OR actor_user_id IS NOT NULL),
    CHECK (decision NOT IN ('rejected', 'revoked', 'superseded') OR reason_code IS NOT NULL)
);

CREATE TABLE email_message (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    task_id UUID NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('sandbox', 'production')),
    policy_version_id UUID NOT NULL,
    approval_id UUID,
    idempotency_key_digest TEXT NOT NULL CHECK (idempotency_key_digest ~ '^sha256:[0-9a-f]{64}$'),
    request_digest TEXT NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    provider_route_id UUID NOT NULL,
    sender_identity_id UUID NOT NULL,
    recipient_count INTEGER NOT NULL CHECK (recipient_count BETWEEN 1 AND 1000),
    intended_to JSONB NOT NULL CHECK (jsonb_typeof(intended_to) = 'array'),
    effective_to JSONB NOT NULL CHECK (jsonb_typeof(effective_to) = 'array'),
    subject_digest TEXT NOT NULL CHECK (subject_digest ~ '^sha256:[0-9a-f]{64}$'),
    body_digest TEXT NOT NULL CHECK (body_digest ~ '^sha256:[0-9a-f]{64}$'),
    encrypted_payload_ref TEXT NOT NULL CHECK (char_length(encrypted_payload_ref) BETWEEN 1 AND 2048),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (
        status IN ('queued', 'sending', 'accepted', 'failed_permanent', 'ambiguous', 'dead', 'cancelled')
    ),
    attempt_count SMALLINT NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 20),
    lease_token UUID,
    lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    lease_expires_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    provider_message_id TEXT CHECK (
        provider_message_id IS NULL OR char_length(provider_message_id) BETWEEN 1 AND 512
    ),
    provider_status TEXT CHECK (provider_status IS NULL OR char_length(provider_status) BETWEEN 1 AND 128),
    last_error_code TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 128),
    authorized_at TIMESTAMPTZ,
    authorized_lease_generation BIGINT CHECK (authorized_lease_generation >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CHECK ((status = 'sending') = (lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK ((authorized_at IS NULL) = (authorized_lease_generation IS NULL)),
    CHECK (authorized_lease_generation IS NULL OR authorized_lease_generation = lease_generation),
    CHECK ((status = 'accepted') = (accepted_at IS NOT NULL)),
    CHECK (
        (status IN ('accepted', 'failed_permanent', 'ambiguous', 'dead', 'cancelled'))
        = (completed_at IS NOT NULL)
    ),
    CHECK ((mode = 'production' AND approval_id IS NOT NULL) OR (mode = 'sandbox' AND approval_id IS NULL))
);

CREATE TABLE agent_email_quota_reservation (
    workspace_id UUID NOT NULL,
    message_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    provider_route_id UUID NOT NULL,
    sender_identity_id UUID NOT NULL,
    recipient_count INTEGER NOT NULL CHECK (recipient_count BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE email_delivery_recipient (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    message_id UUID NOT NULL,
    recipient_digest TEXT NOT NULL CHECK (recipient_digest ~ '^sha256:[0-9a-f]{64}$'),
    encrypted_recipient_ref TEXT NOT NULL CHECK (char_length(encrypted_recipient_ref) BETWEEN 1 AND 2048),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'accepted', 'delivered', 'bounced', 'failed_permanent', 'ambiguous')
    ),
    provider_recipient_id TEXT CHECK (
        provider_recipient_id IS NULL OR char_length(provider_recipient_id) BETWEEN 1 AND 512
    ),
    last_error_code TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CHECK ((status IN ('pending', 'accepted', 'ambiguous')) OR completed_at IS NOT NULL)
);

CREATE FUNCTION reject_agent_email_append_only_update()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('multica.workspace_teardown', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% rows are append-only', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER trg_agent_email_policy_version_append_only
BEFORE UPDATE OR DELETE ON agent_email_policy_version
FOR EACH ROW EXECUTE FUNCTION reject_agent_email_append_only_update();

CREATE TRIGGER trg_agent_email_approval_append_only
BEFORE UPDATE OR DELETE ON agent_email_approval
FOR EACH ROW EXECUTE FUNCTION reject_agent_email_append_only_update();

CREATE FUNCTION enforce_agent_email_task_pin_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.email_agent_config_digest IS NOT NULL
       AND NEW.email_agent_config_digest IS DISTINCT FROM OLD.email_agent_config_digest THEN
        RAISE EXCEPTION 'agent email task config digest is immutable once pinned';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_agent_email_task_pin_immutable
BEFORE UPDATE OF email_agent_config_digest ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION enforce_agent_email_task_pin_immutable();

-- This function is the hard quota reservation boundary. All callers acquire
-- the same four locks in the same order. Provider and sender totals are global;
-- workspace and Agent totals remain explicitly tenant-scoped.
CREATE FUNCTION reserve_agent_email_quota(
    p_workspace_id UUID,
    p_message_id UUID,
    p_agent_id UUID,
    p_provider_route_id UUID,
    p_sender_identity_id UUID,
    p_recipient_count INTEGER,
    p_provider_per_minute BIGINT,
    p_provider_per_day BIGINT,
    p_workspace_per_minute BIGINT,
    p_workspace_per_day BIGINT,
    p_agent_per_minute BIGINT,
    p_agent_per_day BIGINT,
    p_sender_per_minute BIGINT,
    p_sender_per_day BIGINT
) RETURNS BOOLEAN
LANGUAGE plpgsql
AS $$
DECLARE
    minute_start TIMESTAMPTZ := date_trunc('minute', statement_timestamp());
    day_start TIMESTAMPTZ := date_trunc('day', statement_timestamp());
    used BIGINT;
BEGIN
    IF p_recipient_count <= 0
       OR p_provider_per_minute <= 0 OR p_provider_per_day <= 0
       OR p_workspace_per_minute <= 0 OR p_workspace_per_day <= 0
       OR p_agent_per_minute <= 0 OR p_agent_per_day <= 0
       OR p_sender_per_minute <= 0 OR p_sender_per_day <= 0 THEN
        RAISE EXCEPTION USING ERRCODE = '22023', MESSAGE = 'invalid agent email quota reservation';
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended('agent-email/provider/' || p_provider_route_id::text, 0));
    PERFORM pg_advisory_xact_lock(hashtextextended('agent-email/sender/' || p_sender_identity_id::text, 0));
    PERFORM pg_advisory_xact_lock(hashtextextended('agent-email/workspace/' || p_workspace_id::text, 0));
    PERFORM pg_advisory_xact_lock(hashtextextended('agent-email/agent/' || p_workspace_id::text || '/' || p_agent_id::text, 0));

    IF EXISTS (
        SELECT 1 FROM agent_email_quota_reservation
        WHERE workspace_id = p_workspace_id AND message_id = p_message_id
    ) THEN
        RETURN FALSE;
    END IF;

    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE provider_route_id = p_provider_route_id AND created_at >= minute_start;
    IF used + p_recipient_count > p_provider_per_minute THEN RAISE EXCEPTION 'agent email provider minute quota exceeded'; END IF;
    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE provider_route_id = p_provider_route_id AND created_at >= day_start;
    IF used + p_recipient_count > p_provider_per_day THEN RAISE EXCEPTION 'agent email provider day quota exceeded'; END IF;

    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE workspace_id = p_workspace_id AND created_at >= minute_start;
    IF used + p_recipient_count > p_workspace_per_minute THEN RAISE EXCEPTION 'agent email workspace minute quota exceeded'; END IF;
    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE workspace_id = p_workspace_id AND created_at >= day_start;
    IF used + p_recipient_count > p_workspace_per_day THEN RAISE EXCEPTION 'agent email workspace day quota exceeded'; END IF;

    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE workspace_id = p_workspace_id AND agent_id = p_agent_id AND created_at >= minute_start;
    IF used + p_recipient_count > p_agent_per_minute THEN RAISE EXCEPTION 'agent email agent minute quota exceeded'; END IF;
    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE workspace_id = p_workspace_id AND agent_id = p_agent_id AND created_at >= day_start;
    IF used + p_recipient_count > p_agent_per_day THEN RAISE EXCEPTION 'agent email agent day quota exceeded'; END IF;

    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE sender_identity_id = p_sender_identity_id AND created_at >= minute_start;
    IF used + p_recipient_count > p_sender_per_minute THEN RAISE EXCEPTION 'agent email sender minute quota exceeded'; END IF;
    SELECT COALESCE(sum(recipient_count), 0) INTO used
    FROM agent_email_quota_reservation
    WHERE sender_identity_id = p_sender_identity_id AND created_at >= day_start;
    IF used + p_recipient_count > p_sender_per_day THEN RAISE EXCEPTION 'agent email sender day quota exceeded'; END IF;

    INSERT INTO agent_email_quota_reservation (
        workspace_id, message_id, agent_id, provider_route_id,
        sender_identity_id, recipient_count, created_at
    ) VALUES (
        p_workspace_id, p_message_id, p_agent_id, p_provider_route_id,
        p_sender_identity_id, p_recipient_count, statement_timestamp()
    );
    RETURN TRUE;
END;
$$;
