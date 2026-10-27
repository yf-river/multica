CREATE UNIQUE INDEX CONCURRENTLY supervision_fingerprint ON supervision_finding(workspace_id,issue_id,fingerprint);
