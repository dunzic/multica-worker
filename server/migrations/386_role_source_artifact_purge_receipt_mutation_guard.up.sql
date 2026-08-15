CREATE OR REPLACE FUNCTION guard_role_source_artifact_purge_receipt_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'role source artifact purge receipts are immutable'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_guard_role_source_artifact_purge_receipt_mutation
BEFORE UPDATE OR DELETE ON role_source_artifact_purge_receipt
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_artifact_purge_receipt_mutation();
