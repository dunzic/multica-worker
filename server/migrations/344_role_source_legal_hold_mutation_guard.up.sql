-- Legal-hold authority is append-only. A released hold may be removed only as
-- part of an authorized tenant teardown; an active hold remains a hard fence
-- even if a legacy cleanup query bypasses the HTTP preflight.
CREATE OR REPLACE FUNCTION guard_role_source_legal_hold_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'role source legal holds are immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM role_source_legal_hold_release release
        WHERE release.hold_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'active role source legal hold cannot be deleted'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER trg_guard_role_source_legal_hold_mutation
BEFORE UPDATE OR DELETE ON role_source_legal_hold
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_legal_hold_mutation();

CREATE OR REPLACE FUNCTION guard_role_source_legal_hold_release_update()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'role source legal hold releases are immutable'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_guard_role_source_legal_hold_release_update
BEFORE UPDATE ON role_source_legal_hold_release
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_legal_hold_release_update();
