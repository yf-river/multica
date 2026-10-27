CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS release_paused_task_key ON release_paused_task(window_id, task_id);
