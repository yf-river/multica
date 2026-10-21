CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_active_issue ON supervision_batch(workspace_id,issue_id) WHERE status IN ('preparing','running');
