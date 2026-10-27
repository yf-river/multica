CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_manual_preparation_open ON supervision_manual_preparation(workspace_id,issue_id) WHERE status='open';
