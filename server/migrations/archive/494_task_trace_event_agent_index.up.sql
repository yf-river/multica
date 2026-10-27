CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_trace_event_agent_created ON task_trace_event(agent_id, created_at DESC);
