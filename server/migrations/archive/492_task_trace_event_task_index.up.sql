CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_trace_event_task_created ON task_trace_event(task_id, created_at DESC);
