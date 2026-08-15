-- The validated CASE-guarded constraints now carry the state/length contract.
-- Drop the original constraints last so an upgrade never has an unguarded gap.
ALTER TABLE role_source_runtime_attestation
    DROP CONSTRAINT role_source_runtime_attestation_check;

ALTER TABLE role_source_runtime_attestation_observation
    DROP CONSTRAINT role_source_runtime_attestation_observation_check;
