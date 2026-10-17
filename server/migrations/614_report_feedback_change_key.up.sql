CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS report_feedback_change_key ON report_feedback_change (workspace_id, attachment_id, id);
