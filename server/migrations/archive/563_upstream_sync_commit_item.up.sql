CREATE INDEX CONCURRENTLY upstream_sync_commit_item ON upstream_sync_commit(item_id) WHERE item_id IS NOT NULL;
