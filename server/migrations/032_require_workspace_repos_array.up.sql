-- workspace.repos has one current persisted shape: a JSON array. Older
-- deployments could contain objects despite the column's [] default; normalize
-- those rows once so response code can rely on the database invariant.
UPDATE workspace
SET repos = '[]'::jsonb
WHERE jsonb_typeof(repos) IS DISTINCT FROM 'array';

ALTER TABLE workspace
DROP CONSTRAINT IF EXISTS workspace_repos_array;

ALTER TABLE workspace
ADD CONSTRAINT workspace_repos_array
CHECK (jsonb_typeof(repos) = 'array');
