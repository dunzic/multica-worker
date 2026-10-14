DROP TRIGGER IF EXISTS trg_agent_email_task_authorization_snapshot ON agent_task_queue;
DROP FUNCTION IF EXISTS capture_agent_email_task_authorization_snapshot();
DROP TRIGGER IF EXISTS trg_agent_email_policy_prompt_invalidation ON agent;
DROP FUNCTION IF EXISTS invalidate_agent_email_policy_on_prompt_change();
DROP TRIGGER IF EXISTS trg_agent_email_task_pin_immutable ON agent_task_queue;
DROP FUNCTION IF EXISTS enforce_agent_email_task_pin_immutable();

ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_email_authorization_snapshot_shape,
    DROP COLUMN IF EXISTS email_agent_approval_id,
    DROP COLUMN IF EXISTS email_agent_policy_version_id;

CREATE FUNCTION enforce_agent_email_task_pin_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.email_agent_config_digest IS NOT NULL
       AND NEW.email_agent_config_digest IS DISTINCT FROM OLD.email_agent_config_digest THEN
        RAISE EXCEPTION 'agent email task config digest is immutable once pinned';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_agent_email_task_pin_immutable
BEFORE UPDATE OF email_agent_config_digest ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION enforce_agent_email_task_pin_immutable();
