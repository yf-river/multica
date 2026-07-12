-- The data normalization is intentionally one-way. Rolling back only removes
-- the object constraint; invalid metadata cannot be reconstructed.
ALTER TABLE agent_runtime
DROP CONSTRAINT IF EXISTS agent_runtime_metadata_object;
