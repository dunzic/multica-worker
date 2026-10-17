CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS report_feedback_row_key ON report_feedback_row (workspace_id, attachment_id, id);
