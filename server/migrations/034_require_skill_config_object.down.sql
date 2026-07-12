-- The data normalization is intentionally one-way. Rolling back only removes
-- the shape constraint; it cannot reconstruct invalid scalar/array config.
ALTER TABLE skill
DROP CONSTRAINT IF EXISTS skill_config_object;
