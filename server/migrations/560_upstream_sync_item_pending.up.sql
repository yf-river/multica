CREATE INDEX CONCURRENTLY upstream_sync_item_pending ON upstream_sync_item(workspace_id, status, verdict, created_at DESC);
