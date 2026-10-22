CREATE UNIQUE INDEX CONCURRENTLY upstream_sync_commit_unique ON upstream_sync_commit(workspace_id, commit_sha);
