CREATE INDEX CONCURRENTLY role_source_mapping_listing_idx ON role_source_object_mapping (workspace_id, source_id, source_kind, source_parent_id, source_object_id);
