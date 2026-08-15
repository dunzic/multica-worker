-- Content-addressed role-source bodies are stored outside PostgreSQL. This
-- table is the tenant-scoped, immutable readiness ledger used before a
-- snapshot is accepted or an apply reads an object. The storage key is server
-- generated and never exposed through member APIs.
CREATE TABLE role_source_artifact (
    workspace_id UUID NOT NULL,
    digest TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 0 AND 1073741824),
    storage_key TEXT NOT NULL CHECK (char_length(storage_key) BETWEEN 1 AND 1024),
    uploaded_by_runtime_id UUID NOT NULL,
    first_source_id UUID NOT NULL,
    first_scan_request_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
