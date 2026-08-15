-- Constraint validation is metadata-only and cannot be meaningfully undone.
-- Migration 388 owns the schema rollback after proving no ambiguous rows exist.
SELECT 1;
