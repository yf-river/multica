CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS release_window_active ON release_window(environment_id) WHERE state <> 'finished';
