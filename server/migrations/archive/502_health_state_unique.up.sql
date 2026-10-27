CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS health_state_scope_idx ON health_state(workspace_id,user_id);
