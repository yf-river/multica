CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS health_revision_scope_idx ON health_revision(workspace_id,user_id,revision);
