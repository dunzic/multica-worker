CREATE UNIQUE INDEX CONCURRENTLY email_delivery_recipient_message_uidx ON email_delivery_recipient (workspace_id, message_id, recipient_digest);
