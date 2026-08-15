CREATE INDEX CONCURRENTLY role_source_retention_candidate_claim_idx ON role_source_retention_candidate (state, next_attempt_at, created_at, id);
