DROP TRIGGER IF EXISTS project_repository_migration_guard ON project_resource;
DROP TRIGGER IF EXISTS repository_location_migration_guard ON repository_location;
DROP TRIGGER IF EXISTS repository_migration_guard ON repository;
DROP FUNCTION IF EXISTS guard_repository_migration();
DROP TABLE IF EXISTS repository_migration_state;
