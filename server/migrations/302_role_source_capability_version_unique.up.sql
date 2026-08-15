CREATE UNIQUE INDEX CONCURRENTLY role_source_capability_version_unique ON role_source_capability_version (source_id, capability_id, version, object_digest);
