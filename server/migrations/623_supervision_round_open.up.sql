CREATE UNIQUE INDEX CONCURRENTLY supervision_round_open ON supervision_round(workspace_id, issue_id) WHERE status = 'open';
