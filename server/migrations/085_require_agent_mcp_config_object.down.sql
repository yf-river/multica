-- Data normalization is intentionally one-way; rollback only removes the
-- write constraint and cannot reconstruct invalid historical JSON shapes.
ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_mcp_config_object;
