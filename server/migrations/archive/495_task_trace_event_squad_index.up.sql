CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_trace_event_squad_created ON task_trace_event(squad_id, created_at DESC);
