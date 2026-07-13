-- Data normalization is one-way; rollback only removes the shape constraint.
ALTER TABLE autopilot_trigger
DROP CONSTRAINT IF EXISTS autopilot_trigger_event_filters_array;
