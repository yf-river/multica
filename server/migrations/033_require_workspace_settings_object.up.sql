-- workspace.settings is a keyed settings document. Normalize values written
-- through the former untyped PATCH boundary once, then enforce the object
-- shape used by the Go response and Core schema.
UPDATE workspace
SET settings = '{}'::jsonb
WHERE jsonb_typeof(settings) IS DISTINCT FROM 'object';

ALTER TABLE workspace
DROP CONSTRAINT IF EXISTS workspace_settings_object;

ALTER TABLE workspace
ADD CONSTRAINT workspace_settings_object
CHECK (jsonb_typeof(settings) = 'object');
