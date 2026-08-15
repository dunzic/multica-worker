ALTER TABLE role_source_plan_approval
    ADD COLUMN request_key TEXT;

UPDATE role_source_plan_approval
SET request_key = id::text
WHERE request_key IS NULL;

ALTER TABLE role_source_plan_approval
    ALTER COLUMN request_key SET NOT NULL;

ALTER TABLE role_source_plan_approval
    ADD CONSTRAINT role_source_plan_approval_request_key_check
    CHECK (char_length(request_key) BETWEEN 1 AND 200);
