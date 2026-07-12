-- The data normalization is intentionally one-way. Rolling back only removes
-- the shape constraint; it cannot reconstruct invalid scalar/array settings.
ALTER TABLE workspace
DROP CONSTRAINT IF EXISTS workspace_settings_object;
