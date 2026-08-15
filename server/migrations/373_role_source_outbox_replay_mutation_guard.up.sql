CREATE OR REPLACE FUNCTION guard_role_source_outbox_replay_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('multica.workspace_teardown', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'role source outbox replay receipts are immutable'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_guard_role_source_outbox_replay_mutation
BEFORE UPDATE OR DELETE ON role_source_outbox_replay
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_outbox_replay_mutation();
