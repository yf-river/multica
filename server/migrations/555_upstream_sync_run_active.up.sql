CREATE UNIQUE INDEX CONCURRENTLY upstream_sync_run_active ON upstream_sync_run(workspace_id) WHERE status IN ('preparing','running');
