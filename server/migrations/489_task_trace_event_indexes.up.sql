CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_trace_event_workspace_created ON task_trace_event(workspace_id, created_at DESC);
