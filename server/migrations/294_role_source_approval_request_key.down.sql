ALTER TABLE role_source_plan_approval
    DROP CONSTRAINT role_source_plan_approval_request_key_check,
    DROP COLUMN request_key;
