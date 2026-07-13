DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM project_resource
        WHERE resource_type NOT IN ('github_repo', 'gongfeng_repo', 'local_directory')
           OR jsonb_typeof(resource_ref) IS DISTINCT FROM 'object'
    ) THEN
        RAISE EXCEPTION 'project_resource contains rows outside the current resource contract'
            USING HINT = 'Inspect and repair or explicitly remove invalid project_resource rows before retrying migration 100.';
    END IF;
END
$$;

ALTER TABLE project_resource DROP CONSTRAINT IF EXISTS project_resource_ref_is_object;
ALTER TABLE project_resource ADD CONSTRAINT project_resource_ref_is_object
CHECK (jsonb_typeof(resource_ref) = 'object');

ALTER TABLE project_resource DROP CONSTRAINT IF EXISTS project_resource_type_check;
ALTER TABLE project_resource ADD CONSTRAINT project_resource_type_check
CHECK (resource_type IN ('github_repo', 'gongfeng_repo', 'local_directory'));
