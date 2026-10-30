CREATE UNIQUE INDEX CONCURRENTLY upstream_deployment_active ON upstream_deployment(project_id) WHERE status IN ('pending','running');
