ALTER TABLE role_source_runtime_attestation
    ADD CONSTRAINT role_source_runtime_attestation_check CHECK (
        (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
        OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
    ) NOT VALID;

ALTER TABLE role_source_runtime_attestation_observation
    ADD CONSTRAINT role_source_runtime_attestation_observation_check CHECK (
        (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
        OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
    ) NOT VALID;
