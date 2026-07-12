-- The data normalization is intentionally one-way. Rolling back only removes
-- the shape constraint; it cannot reconstruct invalid scalar/array config.
ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_runtime_config_object;
