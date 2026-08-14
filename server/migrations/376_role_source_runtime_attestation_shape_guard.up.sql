-- Add the fail-closed replacement before removing the original shape checks.
-- CASE prevents jsonb_array_length from being evaluated for a scalar value;
-- malformed internal writes therefore produce SQLSTATE 23514 instead of a
-- statement-level SQLSTATE 22023 error.
ALTER TABLE role_source_runtime_attestation
    ADD CONSTRAINT role_source_runtime_attestation_shape_check CHECK (
        CASE
            WHEN jsonb_typeof(sources) = 'array' THEN
                (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
                OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
            ELSE FALSE
        END
    ) NOT VALID;

ALTER TABLE role_source_runtime_attestation_observation
    ADD CONSTRAINT role_source_runtime_attestation_observation_shape_check CHECK (
        CASE
            WHEN jsonb_typeof(sources) = 'array' THEN
                (loaded AND config_revision IS NOT NULL AND jsonb_array_length(sources) BETWEEN 1 AND 512)
                OR (NOT loaded AND config_revision IS NULL AND jsonb_array_length(sources) = 0)
            ELSE FALSE
        END
    ) NOT VALID;
