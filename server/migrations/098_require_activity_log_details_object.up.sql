UPDATE activity_log SET details = '{}'::jsonb
WHERE jsonb_typeof(details) IS DISTINCT FROM 'object';

ALTER TABLE activity_log DROP CONSTRAINT IF EXISTS activity_log_details_is_object;
ALTER TABLE activity_log ADD CONSTRAINT activity_log_details_is_object
CHECK (jsonb_typeof(details) = 'object');
