-- Keep the existing-row scans out of migration 390's metadata-lock window.
ALTER TABLE channel_delivery
    VALIDATE CONSTRAINT channel_delivery_status_check,
    VALIDATE CONSTRAINT channel_delivery_reconciliation_count_check,
    VALIDATE CONSTRAINT channel_delivery_reconciliation_state_check,
    VALIDATE CONSTRAINT channel_delivery_retry_publish_lease_check;
