-- The data normalization is intentionally one-way. Rolling back only removes
-- the shape constraint; it cannot reconstruct invalid repository objects.
ALTER TABLE workspace
DROP CONSTRAINT IF EXISTS workspace_repos_array;
