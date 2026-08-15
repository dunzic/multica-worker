CREATE UNIQUE INDEX CONCURRENTLY idx_channel_delivery_external ON channel_delivery (installation_id, external_message_id) WHERE installation_id IS NOT NULL AND external_message_id IS NOT NULL;
