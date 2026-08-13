-- Retention authority is append-only. A new revision supersedes an older one;
-- history may be removed only as part of the guarded workspace teardown path.
CREATE OR REPLACE FUNCTION guard_role_source_retention_policy_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('multica.workspace_teardown', true) = 'on' THEN
        RETURN OLD;
    END IF;

    RAISE EXCEPTION 'role source retention policy revisions are append-only'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_guard_role_source_retention_policy_mutation
BEFORE UPDATE OR DELETE ON role_source_retention_policy
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_retention_policy_mutation();
