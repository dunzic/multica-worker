-- The state table is removed by migration 354. Keep rows across a partial
-- rollback so reapplying 357 does not discard verification history.
SELECT 1;
