-- Agent MCP configuration has one current persisted shape: either no config
-- (SQL NULL) or a JSON object keyed by MCP server name. Normalize historical
-- scalar/array values once, then reject future writes outside that contract.
UPDATE agent
SET mcp_config = NULL
WHERE mcp_config IS NOT NULL
  AND jsonb_typeof(mcp_config) IS DISTINCT FROM 'object';

ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_mcp_config_object;

ALTER TABLE agent
ADD CONSTRAINT agent_mcp_config_object CHECK (
    mcp_config IS NULL OR jsonb_typeof(mcp_config) = 'object'
);
