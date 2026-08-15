CREATE UNIQUE INDEX CONCURRENTLY role_source_mapping_identity_unique ON role_source_object_mapping (source_id, source_kind, source_parent_id, source_object_id);
