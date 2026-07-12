-- The data normalization is intentionally one-way. Rolling back only removes
-- the shape constraints; invalid custom state cannot be reconstructed.
ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_custom_env_string_object,
DROP CONSTRAINT IF EXISTS agent_custom_args_string_array;
