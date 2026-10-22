CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_quality_supervision_unique
    ON agent (workspace_id) WHERE system_key = 'quality_supervision';
