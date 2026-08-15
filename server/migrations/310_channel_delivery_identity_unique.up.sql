CREATE UNIQUE INDEX CONCURRENTLY idx_channel_delivery_identity ON channel_delivery (installation_id, task_id, operation_kind) WHERE installation_id IS NOT NULL;
