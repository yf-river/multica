CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_active_workspace ON supervision_batch(workspace_id) WHERE status IN ('preparing','running');
