CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS upstream_sync_absorption_task_item
    ON upstream_sync_absorption_task(item_id);
