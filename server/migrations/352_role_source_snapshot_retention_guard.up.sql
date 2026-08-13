-- A task pin takes a shared lock on its immutable snapshot. A retention DELETE
-- takes the conflicting row lock, so pin creation and pruning cannot cross and
-- leave an orphan under READ COMMITTED.
CREATE OR REPLACE FUNCTION guard_role_source_task_pin_snapshot()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM 1
    FROM role_source_snapshot snapshot
    WHERE snapshot.workspace_id = NEW.workspace_id
      AND snapshot.source_id = NEW.source_id
      AND snapshot.snapshot_digest = NEW.snapshot_digest
    FOR KEY SHARE OF snapshot;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'role source task pin requires retained snapshot provenance'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_guard_role_source_task_pin_snapshot
BEFORE INSERT ON role_source_task_pin
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_task_pin_snapshot();

CREATE OR REPLACE FUNCTION guard_role_source_snapshot_retention()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'role source snapshots are immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF current_setting('multica.workspace_teardown', true) = 'on' THEN
        IF EXISTS (
            SELECT 1 FROM role_source_legal_hold hold
            WHERE hold.workspace_id = OLD.workspace_id
              AND NOT EXISTS (
                  SELECT 1 FROM role_source_legal_hold_release release
                  WHERE release.hold_id = hold.id
              )
        ) THEN
            RAISE EXCEPTION 'active legal hold prevents workspace snapshot deletion'
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF current_setting('multica.role_source_retention_prune', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'role source snapshot deletion requires retention authority'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    IF EXISTS (
        SELECT 1 FROM role_source source
        WHERE source.id = OLD.source_id AND source.workspace_id = OLD.workspace_id
          AND source.current_snapshot_digest = OLD.snapshot_digest
    ) OR EXISTS (
        SELECT 1 FROM role_source_task_pin pin
        WHERE pin.source_id = OLD.source_id AND pin.workspace_id = OLD.workspace_id
          AND pin.snapshot_digest = OLD.snapshot_digest
    ) OR EXISTS (
        SELECT 1 FROM role_source_object_mapping mapping
        WHERE mapping.source_id = OLD.source_id AND mapping.workspace_id = OLD.workspace_id
          AND mapping.last_snapshot_digest = OLD.snapshot_digest
    ) OR EXISTS (
        SELECT 1 FROM role_source_legal_hold hold
        WHERE hold.source_id = OLD.source_id AND hold.workspace_id = OLD.workspace_id
          AND (hold.scope = 'source' OR hold.snapshot_digest = OLD.snapshot_digest)
          AND NOT EXISTS (
              SELECT 1 FROM role_source_legal_hold_release release
              WHERE release.hold_id = hold.id
          )
    ) OR EXISTS (
        SELECT 1 FROM role_source_secret_transfer transfer
        WHERE transfer.source_id = OLD.source_id AND transfer.workspace_id = OLD.workspace_id
          AND transfer.snapshot_digest = OLD.snapshot_digest
          AND transfer.status IN ('pending', 'claimed', 'submitted')
    ) OR EXISTS (
        SELECT 1 FROM role_source_apply apply
        WHERE apply.source_id = OLD.source_id AND apply.workspace_id = OLD.workspace_id
          AND apply.snapshot_digest = OLD.snapshot_digest
          AND apply.status IN ('pending', 'running')
    ) THEN
        RAISE EXCEPTION 'role source snapshot remains protected'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER trg_guard_role_source_snapshot_retention
BEFORE UPDATE OR DELETE ON role_source_snapshot
FOR EACH ROW
EXECUTE FUNCTION guard_role_source_snapshot_retention();
