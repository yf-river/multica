-- 1. Single statement on purpose: a multi-statement file runs as one implicit
--    transaction, where CREATE INDEX CONCURRENTLY is rejected, and only this
--    form is treated as an additive change by the release gate. Listing a task's
--    text changes is the only read path, so this is the table's only index.
CREATE INDEX CONCURRENTLY IF NOT EXISTS run_text_change_task_idx ON public.run_text_change (task_id, created_at);
