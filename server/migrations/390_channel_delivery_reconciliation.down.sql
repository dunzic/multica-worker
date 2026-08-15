DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM channel_delivery_reconciliation)
       OR EXISTS (
           SELECT 1 FROM channel_delivery
           WHERE reconciliation_count > 0
              OR status IN ('retry_authorized', 'reconciled')
       ) THEN
        RAISE EXCEPTION 'cannot remove channel delivery reconciliation while receipts or resolved rows exist';
    END IF;
END $$;

DROP TABLE IF EXISTS channel_delivery_reconciliation;

ALTER TABLE channel_delivery
    DROP CONSTRAINT IF EXISTS channel_delivery_retry_publish_lease_check,
    DROP CONSTRAINT IF EXISTS channel_delivery_reconciliation_state_check,
    DROP CONSTRAINT IF EXISTS channel_delivery_reconciliation_count_check,
    DROP CONSTRAINT channel_delivery_status_check,
    DROP COLUMN IF EXISTS retry_publish_expires_at,
    DROP COLUMN IF EXISTS retry_publish_token,
    DROP COLUMN IF EXISTS last_reconciled_at,
    DROP COLUMN IF EXISTS reconciliation_count,
    ADD CONSTRAINT channel_delivery_status_check
        CHECK (status IN ('pending', 'delivered', 'readback', 'failed', 'ambiguous')) NOT VALID;
