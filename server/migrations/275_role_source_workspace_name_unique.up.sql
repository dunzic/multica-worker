CREATE UNIQUE INDEX CONCURRENTLY role_source_workspace_name_unique ON role_source (workspace_id, lower(name));
