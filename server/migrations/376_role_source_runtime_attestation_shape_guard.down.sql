ALTER TABLE role_source_runtime_attestation
    DROP CONSTRAINT IF EXISTS role_source_runtime_attestation_shape_check;

ALTER TABLE role_source_runtime_attestation_observation
    DROP CONSTRAINT IF EXISTS role_source_runtime_attestation_observation_shape_check;
