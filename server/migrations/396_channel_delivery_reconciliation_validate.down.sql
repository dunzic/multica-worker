ALTER TABLE channel_delivery
    DROP CONSTRAINT IF EXISTS channel_delivery_retry_publish_lease_check,
    DROP CONSTRAINT IF EXISTS channel_delivery_reconciliation_state_check,
    DROP CONSTRAINT IF EXISTS channel_delivery_reconciliation_count_check,
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
