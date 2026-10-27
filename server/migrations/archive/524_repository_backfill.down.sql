UPDATE project_resource p SET resource_ref=b.data->'resource_ref'
FROM repository_migration_backup b WHERE b.kind='project_resource' AND b.id=p.id;
