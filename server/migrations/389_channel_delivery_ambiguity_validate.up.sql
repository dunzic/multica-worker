-- Validation scans existing delivery evidence under SHARE UPDATE EXCLUSIVE
-- rather than extending migration 388's ACCESS EXCLUSIVE metadata lock.
ALTER TABLE channel_delivery
    VALIDATE CONSTRAINT channel_delivery_status_check;

ALTER TABLE channel_delivery
    VALIDATE CONSTRAINT channel_delivery_ambiguous_evidence_check;
