CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_round_sequence ON supervision_round(workspace_id, issue_id, sequence);
