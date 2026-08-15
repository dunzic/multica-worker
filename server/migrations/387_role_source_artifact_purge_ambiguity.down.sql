-- A v2 digest commits to the ambiguity fields. Refuse a rollback that would
-- silently strip those fields and leave unverifiable immutable receipts.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM role_source_artifact_purge_receipt
        WHERE contract_version = 'role-source-artifact-purge-receipt-v2'
    ) THEN
        RAISE EXCEPTION 'cannot remove artifact purge ambiguity fields while v2 receipts exist';
    END IF;
END
$$;

ALTER TABLE role_source_artifact_purge_receipt
    DROP CONSTRAINT IF EXISTS role_source_artifact_purge_receipt_ambiguity_check,
    DROP COLUMN IF EXISTS provider_evidence_complete,
    DROP COLUMN IF EXISTS ambiguous_attempts,
    DROP COLUMN IF EXISTS contract_version;

ALTER TABLE role_source_artifact_delete_intent
    DROP CONSTRAINT IF EXISTS role_source_artifact_delete_intent_purge_ambiguity_check,
    DROP COLUMN IF EXISTS purge_ambiguous_attempts;
