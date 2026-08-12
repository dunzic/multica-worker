CREATE INDEX CONCURRENTLY role_source_secret_transfer_expiry_idx ON role_source_secret_transfer (expires_at, id) WHERE status IN ('pending', 'claimed', 'submitted');
