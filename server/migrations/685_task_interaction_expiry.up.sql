CREATE INDEX CONCURRENTLY IF NOT EXISTS task_interaction_suspended_expiry ON task_interaction (expires_at, task_id) WHERE status = 'pending' AND delivery_mode = 'suspended';
