CREATE UNIQUE INDEX CONCURRENTLY upstream_sync_run_task ON upstream_sync_run(task_id) WHERE task_id IS NOT NULL;
