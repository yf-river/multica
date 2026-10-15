CREATE UNIQUE INDEX CONCURRENTLY repository_location_daemon_idx ON repository_location (workspace_id, repository_id, daemon_id);
