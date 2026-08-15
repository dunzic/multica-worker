ALTER TABLE role_source_scan_request
ADD COLUMN request_key_digest TEXT CHECK (request_key_digest IS NULL OR request_key_digest ~ '^sha256:[0-9a-f]{64}$');
