-- Playground variables are a keyed render context. Normalize retired invalid
-- snapshots once, then prevent response-time false defaults.
UPDATE agent_playground_input
SET variables = '{}'::jsonb
WHERE jsonb_typeof(variables) IS DISTINCT FROM 'object';

ALTER TABLE agent_playground_input
DROP CONSTRAINT IF EXISTS agent_playground_input_variables_object;

ALTER TABLE agent_playground_input
ADD CONSTRAINT agent_playground_input_variables_object
CHECK (jsonb_typeof(variables) = 'object');
