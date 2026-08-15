CREATE INDEX CONCURRENTLY role_source_secret_transfer_claim_idx ON role_source_secret_transfer (runtime_id, status, expires_at, created_at, id);
