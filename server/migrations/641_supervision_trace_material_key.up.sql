CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_trace_material_run_key
    ON supervision_trace_material (workspace_id, run_id, source);
