-- Preserve uncertainty from a provider mutation whose response was partial,
-- lost or abandoned with an expired deleting lease. A later empty inventory
-- still proves exact-key absence, but version/byte counters are then observed
-- lower bounds rather than complete provider-operation evidence.
ALTER TABLE role_source_artifact_delete_intent
    ADD COLUMN purge_ambiguous_attempts INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT role_source_artifact_delete_intent_purge_ambiguity_check
        CHECK (purge_ambiguous_attempts BETWEEN 0 AND 1000000);

ALTER TABLE role_source_artifact_purge_receipt
    ADD COLUMN contract_version TEXT NOT NULL DEFAULT 'role-source-artifact-purge-receipt-v1',
    ADD COLUMN ambiguous_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN provider_evidence_complete BOOLEAN NOT NULL DEFAULT true,
    ADD CONSTRAINT role_source_artifact_purge_receipt_ambiguity_check CHECK (
        (contract_version = 'role-source-artifact-purge-receipt-v1'
            AND ambiguous_attempts = 0
            AND provider_evidence_complete)
        OR
        (contract_version = 'role-source-artifact-purge-receipt-v2'
            AND ambiguous_attempts BETWEEN 0 AND 1000000
            AND provider_evidence_complete = (ambiguous_attempts = 0))
    );
