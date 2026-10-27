CREATE INDEX CONCURRENTLY IF NOT EXISTS supervision_trace_material_pending
    ON supervision_trace_material (workspace_id, next_attempt_at)
    WHERE status IN ('pending','processing');
