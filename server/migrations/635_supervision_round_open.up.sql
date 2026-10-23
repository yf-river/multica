CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_round_open ON supervision_round(workspace_id, issue_id) WHERE status = 'open';
