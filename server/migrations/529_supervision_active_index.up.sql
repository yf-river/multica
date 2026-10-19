CREATE UNIQUE INDEX CONCURRENTLY supervision_active_workspace ON supervision_batch(workspace_id) WHERE status IN ('preparing','running');
