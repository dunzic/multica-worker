CREATE TABLE report_feedback_schema (
    workspace_id UUID NOT NULL,
    attachment_id UUID NOT NULL,
    fields JSONB NOT NULL CHECK (jsonb_typeof(fields) = 'array'),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE report_feedback_row (
    workspace_id UUID NOT NULL,
    attachment_id UUID NOT NULL,
    id UUID NOT NULL,
    label TEXT NOT NULL,
    source_attachment_id UUID,
    values JSONB NOT NULL CHECK (jsonb_typeof(values) = 'object'),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE report_feedback_change (
    id BIGSERIAL NOT NULL,
    workspace_id UUID NOT NULL,
    attachment_id UUID NOT NULL,
    row_id UUID,
    kind TEXT NOT NULL CHECK (kind IN ('schema', 'create', 'update', 'archive')),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent')),
    actor_id UUID NOT NULL,
    actor_name TEXT NOT NULL,
    before_data JSONB NOT NULL,
    after_data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
