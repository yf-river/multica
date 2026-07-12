-- Neither table has a runtime query, route, service, dynamic registration or
-- framework-owned side effect in the current product. Daemon liveness is
-- represented by agent_runtime; issue relationships currently use parent_id
-- and issue_pull_request. Keeping these empty designs in the current schema
-- advertises capabilities that do not exist and invites new code onto dead
-- models.
DROP TABLE IF EXISTS daemon_connection;
DROP TABLE IF EXISTS issue_dependency;
