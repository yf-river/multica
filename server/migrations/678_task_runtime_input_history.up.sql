CREATE INDEX CONCURRENTLY idx_task_runtime_input_history ON task_runtime_input (task_id, created_at DESC, id DESC);
