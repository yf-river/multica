-- Runtime metadata is a keyed document. Daemon registration already merges
-- only object input; normalize older direct writes once and make the database
-- enforce the shape consumed by Runtime responses and readiness checks.
UPDATE agent_runtime
SET metadata = '{}'::jsonb
WHERE jsonb_typeof(metadata) IS DISTINCT FROM 'object';

ALTER TABLE agent_runtime
DROP CONSTRAINT IF EXISTS agent_runtime_metadata_object;

ALTER TABLE agent_runtime
ADD CONSTRAINT agent_runtime_metadata_object
CHECK (jsonb_typeof(metadata) = 'object');
