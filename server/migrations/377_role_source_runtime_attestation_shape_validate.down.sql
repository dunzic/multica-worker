ALTER TABLE role_source_runtime_attestation
    DROP CONSTRAINT IF EXISTS role_source_runtime_attestation_shape_check,
    ADD CONSTRAINT role_source_runtime_attestation_shape_check CHECK (
        CASE
            WHEN jsonb_typeof(sources) = 'array' THEN
                (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
                OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
            ELSE FALSE
        END
    ) NOT VALID;

ALTER TABLE role_source_runtime_attestation_observation
    DROP CONSTRAINT IF EXISTS role_source_runtime_attestation_observation_shape_check,
    ADD CONSTRAINT role_source_runtime_attestation_observation_shape_check CHECK (
        CASE
            WHEN jsonb_typeof(sources) = 'array' THEN
                (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
                OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
            ELSE FALSE
        END
    ) NOT VALID;
