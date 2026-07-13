UPDATE autopilot_run SET trigger_payload = NULL
WHERE trigger_payload IS NOT NULL
  AND jsonb_typeof(trigger_payload) IS DISTINCT FROM 'object';

ALTER TABLE autopilot_run DROP CONSTRAINT IF EXISTS autopilot_run_trigger_payload_is_object;
ALTER TABLE autopilot_run ADD CONSTRAINT autopilot_run_trigger_payload_is_object
CHECK (trigger_payload IS NULL OR jsonb_typeof(trigger_payload) = 'object');
