CREATE UNIQUE INDEX CONCURRENTLY upstream_sync_run_active ON upstream_sync_run((true)) WHERE status IN ('preparing','running');
