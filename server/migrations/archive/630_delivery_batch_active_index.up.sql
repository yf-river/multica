CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS delivery_batch_active ON delivery_batch(workspace_id) WHERE status IN ('collecting','frozen','preparing','validating','publishing');
