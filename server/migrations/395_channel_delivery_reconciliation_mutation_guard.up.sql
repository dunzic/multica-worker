CREATE OR REPLACE FUNCTION guard_channel_delivery_reconciliation_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('multica.workspace_teardown', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'channel delivery reconciliation receipts are immutable'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;

CREATE TRIGGER trg_guard_channel_delivery_reconciliation_mutation
BEFORE UPDATE OR DELETE ON channel_delivery_reconciliation
FOR EACH ROW
EXECUTE FUNCTION guard_channel_delivery_reconciliation_mutation();
