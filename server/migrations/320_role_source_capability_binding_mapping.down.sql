ALTER TABLE role_source_object_mapping
    DROP CONSTRAINT role_source_object_mapping_source_kind_check,
    ADD CONSTRAINT role_source_object_mapping_source_kind_check
        CHECK (source_kind IN ('role', 'skill', 'automation', 'environment', 'mcp'));
