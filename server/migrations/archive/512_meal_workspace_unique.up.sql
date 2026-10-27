CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS meal_workspace_scope_idx ON meal_workspace(workspace_id, user_id);
