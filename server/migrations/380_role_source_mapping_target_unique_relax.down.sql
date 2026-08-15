CREATE UNIQUE INDEX CONCURRENTLY role_source_mapping_target_unique ON role_source_object_mapping (workspace_id, target_kind, target_id) WHERE source_kind <> 'capability_binding';
