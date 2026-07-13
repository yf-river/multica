-- Data normalization is one-way; rollback only removes the shape constraint.
ALTER TABLE task_trace_event
DROP CONSTRAINT IF EXISTS task_trace_event_metadata_is_object;
