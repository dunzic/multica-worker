DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM channel_delivery WHERE status = 'ambiguous') THEN
        RAISE EXCEPTION 'cannot remove channel delivery ambiguity state while ambiguous evidence exists';
    END IF;
END $$;

ALTER TABLE channel_delivery
    DROP CONSTRAINT channel_delivery_ambiguous_evidence_check,
    DROP CONSTRAINT channel_delivery_status_check,
    ADD CONSTRAINT channel_delivery_status_check
        CHECK (status IN ('pending', 'delivered', 'readback', 'failed')),
    DROP COLUMN ambiguous_at;
