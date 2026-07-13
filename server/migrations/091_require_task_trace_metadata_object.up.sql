-- Trace metadata is consumed as a key/value evidence object. Normalize any
-- historical scalar/array value once, then make read-side silent data loss
-- impossible by enforcing the current shape at the persistence boundary.
UPDATE task_trace_event
SET metadata = '{}'::jsonb
WHERE jsonb_typeof(metadata) IS DISTINCT FROM 'object';

ALTER TABLE task_trace_event
DROP CONSTRAINT IF EXISTS task_trace_event_metadata_is_object;

ALTER TABLE task_trace_event
ADD CONSTRAINT task_trace_event_metadata_is_object
CHECK (jsonb_typeof(metadata) = 'object');
