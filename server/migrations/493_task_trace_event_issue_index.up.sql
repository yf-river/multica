CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_trace_event_issue_created ON task_trace_event(issue_id, created_at DESC);
