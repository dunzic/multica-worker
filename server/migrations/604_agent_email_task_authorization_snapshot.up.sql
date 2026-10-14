-- Pin the exact email policy and approval visible when a task is created.
-- A later approval must not grant production email to an older task.
ALTER TABLE agent_task_queue
    ADD COLUMN email_agent_policy_version_id UUID,
    ADD COLUMN email_agent_approval_id UUID,
    ADD CONSTRAINT agent_task_email_authorization_snapshot_shape CHECK (
        (
            email_agent_config_digest IS NULL
            AND email_agent_policy_version_id IS NULL
            AND email_agent_approval_id IS NULL
        )
        OR (
            email_agent_config_digest IS NOT NULL
            AND email_agent_policy_version_id IS NOT NULL
        )
    ) NOT VALID;

CREATE OR REPLACE FUNCTION enforce_agent_email_task_pin_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.email_agent_config_digest IS NOT NULL
       AND ROW(
           NEW.email_agent_config_digest,
           NEW.email_agent_policy_version_id,
           NEW.email_agent_approval_id
       ) IS DISTINCT FROM ROW(
           OLD.email_agent_config_digest,
           OLD.email_agent_policy_version_id,
           OLD.email_agent_approval_id
       ) THEN
        RAISE EXCEPTION 'agent email task authorization snapshot is immutable once pinned';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_agent_email_task_pin_immutable ON agent_task_queue;
CREATE TRIGGER trg_agent_email_task_pin_immutable
BEFORE UPDATE OF email_agent_config_digest, email_agent_policy_version_id, email_agent_approval_id
ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION enforce_agent_email_task_pin_immutable();

CREATE OR REPLACE FUNCTION capture_agent_email_task_authorization_snapshot()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    pinned_config_digest TEXT;
    pinned_policy_version_id UUID;
    pinned_approval_id UUID;
BEGIN
    -- Automatic retries inherit the exact authorization snapshot. A retry is
    -- not a new opportunity to resolve a newer policy or approval.
    IF NEW.retry_of_task_id IS NOT NULL THEN
        SELECT
            parent.email_agent_config_digest,
            parent.email_agent_policy_version_id,
            parent.email_agent_approval_id
        INTO
            pinned_config_digest,
            pinned_policy_version_id,
            pinned_approval_id
        FROM agent_task_queue AS parent
        WHERE parent.id = NEW.retry_of_task_id
          AND parent.agent_id = NEW.agent_id;
    ELSE
        SELECT
            policy.config_digest,
            policy.id,
            CASE
                WHEN state.state = 'approved'
                 AND latest_decision.decision = 'approved'
                 AND latest_decision.actor_user_id = state.assigned_approver_user_id
                THEN latest_decision.id
                ELSE NULL
            END
        INTO
            pinned_config_digest,
            pinned_policy_version_id,
            pinned_approval_id
        FROM agent AS task_agent
        JOIN agent_email_policy_state AS state
          ON state.workspace_id = task_agent.workspace_id
         AND state.agent_id = task_agent.id
        JOIN agent_email_policy_version AS policy
          ON policy.workspace_id = state.workspace_id
         AND policy.agent_id = state.agent_id
         AND policy.id = state.current_policy_version_id
         AND policy.config_digest = state.current_config_digest
        JOIN plugin_execution_manifest AS manifest
          ON manifest.id = NEW.plugin_execution_manifest_id
         AND manifest.task_id = NEW.id
         AND manifest.workspace_id = task_agent.workspace_id
         AND manifest.agent_id = task_agent.id
        LEFT JOIN LATERAL (
            SELECT decision.id, decision.decision, decision.actor_user_id
            FROM agent_email_approval AS decision
            WHERE decision.workspace_id = state.workspace_id
              AND decision.agent_id = state.agent_id
              AND decision.policy_version_id = policy.id
              AND decision.config_digest = policy.config_digest
              AND decision.approval_authority_digest = state.approval_authority_digest
            ORDER BY decision.created_at DESC, decision.id DESC
            LIMIT 1
        ) AS latest_decision ON TRUE
        WHERE task_agent.id = NEW.agent_id
          AND task_agent.archived_at IS NULL
          AND EXISTS (
              SELECT 1
              FROM jsonb_array_elements(manifest.ordered_contributions) AS contribution(entry)
              WHERE contribution.entry->>'release_id' = policy.plugin_release_id::text
                AND contribution.entry->>'artifact_digest' = policy.plugin_artifact_digest
                AND contribution.entry->>'entry_digest' = policy.plugin_entry_digest
          );
    END IF;

    IF pinned_config_digest IS NOT NULL AND pinned_policy_version_id IS NOT NULL THEN
        UPDATE agent_task_queue
        SET email_agent_config_digest = pinned_config_digest,
            email_agent_policy_version_id = pinned_policy_version_id,
            email_agent_approval_id = pinned_approval_id
        WHERE id = NEW.id
          AND email_agent_config_digest IS NULL;
    END IF;

    RETURN NEW;
END;
$$;

-- Plugin V2 removed plugin_execution_manifest before this feature was merged.
-- Do not attach the legacy capture function: without a verified V2 execution
-- binding, leaving the snapshot null is the fail-closed authorization result.

-- Agent identity and instructions are part of the approved config digest. A
-- direct SQL writer or a future application path must not leave an approved
-- policy active after changing either prompt input.
CREATE FUNCTION invalidate_agent_email_policy_on_prompt_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE agent_email_policy_state
    SET state = CASE WHEN state = 'disabled' THEN 'disabled' ELSE 'draft' END,
        updated_at = now()
    WHERE workspace_id = NEW.workspace_id
      AND agent_id = NEW.id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_agent_email_policy_prompt_invalidation
AFTER UPDATE OF name, instructions ON agent
FOR EACH ROW
WHEN (
    OLD.name IS DISTINCT FROM NEW.name
    OR OLD.instructions IS DISTINCT FROM NEW.instructions
)
EXECUTE FUNCTION invalidate_agent_email_policy_on_prompt_change();
