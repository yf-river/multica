ALTER TABLE issue
    ADD COLUMN work_started_at TIMESTAMPTZ NULL,
    ADD COLUMN work_completed_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS idx_issue_work_started_at
    ON issue (workspace_id, work_started_at)
    WHERE work_started_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_issue_work_completed_at
    ON issue (workspace_id, work_completed_at)
    WHERE work_completed_at IS NOT NULL;
