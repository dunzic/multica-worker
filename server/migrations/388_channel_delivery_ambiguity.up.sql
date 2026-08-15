-- A transport timeout, partial chunk send, lost response or process death may
-- leave provider acceptance unknown. Such rows must never re-enter the normal
-- retry path until an independent reconciliation contract resolves them.
ALTER TABLE channel_delivery
    DROP CONSTRAINT channel_delivery_status_check,
    ADD COLUMN ambiguous_at TIMESTAMPTZ,
    ADD CONSTRAINT channel_delivery_status_check
        CHECK (status IN ('pending', 'delivered', 'readback', 'failed', 'ambiguous')) NOT VALID,
    ADD CONSTRAINT channel_delivery_ambiguous_evidence_check CHECK (
        (status = 'ambiguous'
            AND ambiguous_at IS NOT NULL
            AND last_error_code IS NOT NULL
            AND evidence IS NOT NULL
            AND evidence_digest IS NOT NULL
            AND lease_token IS NULL
            AND lease_expires_at IS NULL)
        OR status <> 'ambiguous'
    ) NOT VALID;
