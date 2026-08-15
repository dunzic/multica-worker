CREATE INDEX CONCURRENTLY idx_channel_delivery_recovery ON channel_delivery (lease_expires_at, id) WHERE status = 'pending';
