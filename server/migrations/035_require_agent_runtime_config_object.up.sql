-- Agent runtime_config is a keyed provider/runtime document in every current
-- caller and response schema. Normalize values accepted by the former untyped
-- Create/Update boundary once, then enforce that single persisted shape.
UPDATE agent
SET runtime_config = '{}'::jsonb
WHERE jsonb_typeof(runtime_config) IS DISTINCT FROM 'object';

ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_runtime_config_object;

ALTER TABLE agent
ADD CONSTRAINT agent_runtime_config_object
CHECK (jsonb_typeof(runtime_config) = 'object');
