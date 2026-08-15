CREATE INDEX CONCURRENTLY idx_channel_delivery_retry_publish_due
    ON channel_delivery (last_reconciled_at, id)
    WHERE status = 'retry_authorized';
