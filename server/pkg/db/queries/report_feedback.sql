-- name: GetReportFeedbackSchema :one
SELECT * FROM report_feedback_schema
WHERE workspace_id = $1 AND attachment_id = $2;

-- name: LockReportFeedbackMember :one
SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2 FOR SHARE;

-- name: CreateReportFeedbackSchema :one
INSERT INTO report_feedback_schema (workspace_id, attachment_id, fields)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, attachment_id) DO NOTHING RETURNING *;

-- name: UpdateReportFeedbackSchema :one
UPDATE report_feedback_schema
SET fields = $3, revision = revision + 1, updated_at = now()
WHERE workspace_id = $1 AND attachment_id = $2 AND revision = $4 RETURNING *;

-- name: HasReportFeedbackRows :one
SELECT EXISTS (SELECT 1 FROM report_feedback_row WHERE workspace_id = $1 AND attachment_id = $2);

-- name: ListReportFeedbackRows :many
SELECT * FROM report_feedback_row
WHERE workspace_id = $1 AND attachment_id = $2 AND archived_at IS NULL
AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id LIMIT $3;

-- name: GetReportFeedbackRow :one
SELECT * FROM report_feedback_row
WHERE workspace_id = $1 AND attachment_id = $2 AND id = $3;

-- name: CreateReportFeedbackRow :one
INSERT INTO report_feedback_row (workspace_id, attachment_id, id, label, source_attachment_id, values)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, attachment_id, id) DO NOTHING RETURNING *;

-- name: UpdateReportFeedbackRow :one
UPDATE report_feedback_row
SET label = $4, source_attachment_id = $5, values = $6, revision = revision + 1, updated_at = now()
WHERE workspace_id = $1 AND attachment_id = $2 AND id = $3 AND revision = $7 AND archived_at IS NULL
RETURNING *;

-- name: ArchiveReportFeedbackRow :one
UPDATE report_feedback_row
SET archived_at = now(), updated_at = now(), revision = revision + 1
WHERE workspace_id = $1 AND attachment_id = $2 AND id = $3 AND revision = $4 AND archived_at IS NULL
RETURNING *;

-- name: CreateReportFeedbackChange :exec
INSERT INTO report_feedback_change (workspace_id, attachment_id, row_id, kind, actor_type, actor_id, actor_name, before_data, after_data)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListReportFeedbackChanges :many
SELECT * FROM report_feedback_change
WHERE workspace_id = $1 AND attachment_id = $2
AND (sqlc.arg(before_id)::bigint = 0 OR id < sqlc.arg(before_id)::bigint)
ORDER BY id DESC LIMIT $3;

-- name: DeleteReportFeedbackForAttachment :exec
WITH deleted_changes AS (
    DELETE FROM report_feedback_change WHERE workspace_id = $1 AND attachment_id = $2
), deleted_rows AS (
    DELETE FROM report_feedback_row WHERE workspace_id = $1 AND attachment_id = $2
)
DELETE FROM report_feedback_schema WHERE report_feedback_schema.workspace_id = $1 AND report_feedback_schema.attachment_id = $2;
