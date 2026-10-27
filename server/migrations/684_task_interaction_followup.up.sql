CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS task_interaction_followup ON task_interaction (followup_task_id) WHERE followup_task_id IS NOT NULL;
