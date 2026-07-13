-- Event filters are meaningful only for webhook triggers and have one current
-- persisted shape. Normalize historical schedule/non-array values once, then
-- enforce the same boundary as the HTTP validator.
UPDATE autopilot_trigger
SET event_filters = NULL
WHERE event_filters IS NOT NULL
  AND (kind <> 'webhook' OR jsonb_typeof(event_filters) IS DISTINCT FROM 'array');

ALTER TABLE autopilot_trigger
DROP CONSTRAINT IF EXISTS autopilot_trigger_event_filters_array;

ALTER TABLE autopilot_trigger
ADD CONSTRAINT autopilot_trigger_event_filters_array CHECK (
    event_filters IS NULL
    OR (kind = 'webhook' AND jsonb_typeof(event_filters) = 'array')
);
