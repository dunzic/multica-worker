-- Centralize provenance capture so every current and future task insertion
-- path receives the same contract. A retry inherits its parent's exact pin;
-- it never re-resolves whichever source snapshot happens to be current later.
CREATE OR REPLACE FUNCTION role_source_agent_state_digest(
    target_agent_id UUID,
    target_source_id UUID,
    target_role_id TEXT
)
RETURNS TEXT
LANGUAGE sql
STABLE
AS $$
    SELECT 'sha256:' || encode(
        digest(
            convert_to(
                jsonb_build_object(
                    'agent', jsonb_build_object(
                        'name', agent.name,
                        'description', agent.description,
                        'runtime_mode', agent.runtime_mode,
                        'runtime_id', agent.runtime_id,
                        'instructions', agent.instructions,
                        'custom_env', agent.custom_env,
                        'mcp_config', agent.mcp_config
                    ),
                    'skills', COALESCE((
                        SELECT jsonb_agg(
                            jsonb_build_object(
                                'source_skill_id', mapping.source_object_id,
                                'target_skill_id', skill.id,
                                'name', skill.name,
                                'description', skill.description,
                                'content', skill.content,
                                'config', skill.config,
                                'associated', EXISTS (
                                    SELECT 1 FROM agent_skill association
                                    WHERE association.agent_id = target_agent_id
                                      AND association.skill_id = skill.id
                                      AND association.enabled
                                ),
                                'files', COALESCE((
                                    SELECT jsonb_agg(
                                        jsonb_build_object('path', file.path, 'content', file.content)
                                        ORDER BY file.path
                                    )
                                    FROM skill_file file
                                    WHERE file.skill_id = skill.id
                                ), '[]'::jsonb)
                            )
                            ORDER BY mapping.source_object_id
                        )
                        FROM role_source_object_mapping mapping
                        JOIN skill ON skill.id = mapping.target_id
                                  AND skill.workspace_id = mapping.workspace_id
                        WHERE mapping.source_id = target_source_id
                          AND mapping.source_kind = 'skill'
                          AND mapping.source_parent_id = target_role_id
                          AND mapping.target_kind = 'skill'
                          AND mapping.archived_at IS NULL
                    ), '[]'::jsonb)
                )::text,
                'UTF8'
            ),
            'sha256'
        ),
        'hex'
    )
    FROM agent
    WHERE agent.id = target_agent_id;
$$;

CREATE OR REPLACE FUNCTION capture_role_source_task_pin()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    managed_mapping role_source_object_mapping%ROWTYPE;
    pinned_capabilities JSONB;
    expected_capability_count INTEGER;
BEGIN
    IF NEW.parent_task_id IS NOT NULL THEN
        INSERT INTO role_source_task_pin (
            task_id, workspace_id, agent_id, source_id, source_role_id,
            snapshot_digest, role_object_digest, target_state_digest, capability_pins,
            inherited_from_task_id
        )
        SELECT
            NEW.id, parent.workspace_id, NEW.agent_id, parent.source_id,
            parent.source_role_id, parent.snapshot_digest,
            parent.role_object_digest, parent.target_state_digest,
            parent.capability_pins, parent.task_id
        FROM role_source_task_pin parent
        WHERE parent.task_id = NEW.parent_task_id;

        IF FOUND THEN
            RETURN NEW;
        END IF;
    END IF;

    SELECT mapping.*
    INTO managed_mapping
    FROM role_source_object_mapping mapping
    WHERE mapping.workspace_id = (
            SELECT agent.workspace_id FROM agent WHERE agent.id = NEW.agent_id
        )
      AND mapping.target_kind = 'agent'
      AND mapping.target_id = NEW.agent_id
      AND mapping.source_kind = 'role'
      AND mapping.archived_at IS NULL;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    -- A source-managed agent must never enqueue from a dangling provenance
    -- row. This lookup also proves that the role still exists in the exact
    -- immutable snapshot named by the mapping.
    IF NOT EXISTS (
        SELECT 1
        FROM role_source_snapshot snapshot,
             jsonb_array_elements(snapshot.manifest->'roles') AS role
        WHERE snapshot.workspace_id = managed_mapping.workspace_id
          AND snapshot.source_id = managed_mapping.source_id
          AND snapshot.snapshot_digest = managed_mapping.last_snapshot_digest
          AND role->>'id' = managed_mapping.source_object_id
    ) THEN
        RAISE EXCEPTION 'source-managed agent has invalid snapshot provenance'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    SELECT COALESCE(
        jsonb_agg(
            jsonb_build_object(
                'capability_id', binding->>'capability_id',
                'skill_id', binding->>'skill_id',
				'target_skill_id', skill_mapping.target_id,
                'profile', binding->>'profile',
                'version_constraint', binding->>'version_constraint',
                'resolved_version', capability->>'version',
                'object_digest', version.object_digest,
                'permission_mode', binding->>'permission_mode',
                'required', COALESCE((binding->>'required')::boolean, false),
                'fallback', COALESCE(binding->>'fallback', '')
            )
            ORDER BY binding->>'capability_id', binding->>'skill_id', binding->>'profile'
        ),
        '[]'::jsonb
    )
    INTO pinned_capabilities
    FROM role_source_snapshot snapshot
    CROSS JOIN LATERAL jsonb_array_elements(snapshot.manifest->'roles') AS role
    CROSS JOIN LATERAL jsonb_array_elements(role->'capability_bindings') AS binding
    JOIN LATERAL jsonb_array_elements(snapshot.manifest->'capabilities') AS capability
      ON capability->>'id' = binding->>'capability_id'
    JOIN role_source_capability_version version
      ON version.workspace_id = snapshot.workspace_id
     AND version.source_id = snapshot.source_id
     AND version.capability_id = capability->>'id'
     AND version.version = capability->>'version'
     AND version.definition = capability
	JOIN role_source_object_mapping skill_mapping
	  ON skill_mapping.workspace_id = snapshot.workspace_id
	 AND skill_mapping.source_id = snapshot.source_id
	 AND skill_mapping.source_kind = 'skill'
	 AND skill_mapping.source_parent_id = role->>'id'
	 AND skill_mapping.source_object_id = binding->>'skill_id'
	 AND skill_mapping.target_kind = 'skill'
	 AND skill_mapping.archived_at IS NULL
	 AND skill_mapping.last_snapshot_digest = snapshot.snapshot_digest
	JOIN role_source_object_mapping binding_mapping
	  ON binding_mapping.workspace_id = snapshot.workspace_id
	 AND binding_mapping.source_id = snapshot.source_id
	 AND binding_mapping.source_kind = 'capability_binding'
	 AND binding_mapping.source_parent_id = role->>'id'
	 AND binding_mapping.source_object_id = 'sha256:' || encode(digest(
	       convert_to(binding->>'capability_id', 'UTF8') || decode('00', 'hex') ||
	       convert_to(binding->>'skill_id', 'UTF8') || decode('00', 'hex') ||
	       convert_to(binding->>'profile', 'UTF8'),
	       'sha256'
	   ), 'hex')
	 AND binding_mapping.target_kind = 'skill'
	 AND binding_mapping.target_id = skill_mapping.target_id
	 AND binding_mapping.archived_at IS NULL
	 AND binding_mapping.last_snapshot_digest = snapshot.snapshot_digest
    WHERE snapshot.workspace_id = managed_mapping.workspace_id
      AND snapshot.source_id = managed_mapping.source_id
      AND snapshot.snapshot_digest = managed_mapping.last_snapshot_digest
      AND role->>'id' = managed_mapping.source_object_id;

    SELECT jsonb_array_length(role->'capability_bindings')
    INTO expected_capability_count
    FROM role_source_snapshot snapshot,
         jsonb_array_elements(snapshot.manifest->'roles') AS role
    WHERE snapshot.workspace_id = managed_mapping.workspace_id
      AND snapshot.source_id = managed_mapping.source_id
      AND snapshot.snapshot_digest = managed_mapping.last_snapshot_digest
      AND role->>'id' = managed_mapping.source_object_id;

    IF jsonb_array_length(pinned_capabilities) <> expected_capability_count THEN
        RAISE EXCEPTION 'source-managed agent has unresolved capability provenance'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    INSERT INTO role_source_task_pin (
        task_id, workspace_id, agent_id, source_id, source_role_id,
        snapshot_digest, role_object_digest, target_state_digest, capability_pins
    ) VALUES (
        NEW.id, managed_mapping.workspace_id, NEW.agent_id,
        managed_mapping.source_id, managed_mapping.source_object_id,
        managed_mapping.last_snapshot_digest,
        managed_mapping.last_applied_digest,
        role_source_agent_state_digest(
            NEW.agent_id, managed_mapping.source_id,
            managed_mapping.source_object_id
        ),
        pinned_capabilities
    );

    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_capture_role_source_task_pin
AFTER INSERT ON agent_task_queue
FOR EACH ROW
EXECUTE FUNCTION capture_role_source_task_pin();

CREATE OR REPLACE FUNCTION reject_role_source_task_pin_update()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'role source task pins are immutable'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_reject_role_source_task_pin_update
BEFORE UPDATE ON role_source_task_pin
FOR EACH ROW
EXECUTE FUNCTION reject_role_source_task_pin_update();

-- Apply updates the materialized agent and its mapping in one transaction.
-- Invalidating pre-start tasks from the OLD mapping in that same transaction
-- closes the check/read race: a daemon cannot finalize a dispatched task after
-- a newer role version commits. Running/waiting tasks already received their
-- payload and intentionally continue under their captured version.
CREATE OR REPLACE FUNCTION invalidate_stale_role_source_tasks()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.source_kind = 'role'
       AND OLD.target_kind = 'agent'
       AND (
           OLD.last_snapshot_digest IS DISTINCT FROM NEW.last_snapshot_digest
           OR OLD.last_applied_digest IS DISTINCT FROM NEW.last_applied_digest
           OR OLD.archived_at IS DISTINCT FROM NEW.archived_at
       ) THEN
        UPDATE agent_task_queue task
        SET status = 'cancelled',
            completed_at = now(),
            error = 'source-managed role changed after task enqueue',
            failure_reason = 'role_source_version_stale',
            prepare_lease_expires_at = NULL
        FROM role_source_task_pin pin
        WHERE pin.task_id = task.id
          AND pin.source_id = OLD.source_id
          AND pin.source_role_id = OLD.source_object_id
          AND pin.agent_id = OLD.target_id
          AND pin.snapshot_digest = OLD.last_snapshot_digest
          AND pin.role_object_digest = OLD.last_applied_digest
          AND task.status IN ('queued', 'deferred', 'dispatched');
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_invalidate_stale_role_source_tasks
BEFORE UPDATE ON role_source_object_mapping
FOR EACH ROW
EXECUTE FUNCTION invalidate_stale_role_source_tasks();
