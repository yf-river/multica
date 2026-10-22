CREATE INDEX CONCURRENTLY upstream_sync_run_list ON upstream_sync_run(workspace_id, created_at DESC);
