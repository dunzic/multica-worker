-- Validation only changes constraint metadata. Migration 418 removes the
-- constraint when the feature schema is rolled back.
SELECT 1;
