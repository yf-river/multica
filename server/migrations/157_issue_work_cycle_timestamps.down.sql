DROP INDEX IF EXISTS idx_issue_work_completed_at;
DROP INDEX IF EXISTS idx_issue_work_started_at;

ALTER TABLE issue
    DROP COLUMN IF EXISTS work_completed_at,
    DROP COLUMN IF EXISTS work_started_at;
