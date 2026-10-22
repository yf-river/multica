UPDATE agent
SET system_key = 'upstream_sync:' || COALESCE(runtime_id::text, ''), kind = 'system', updated_at = now()
WHERE system_key = 'upstream_sync';

UPDATE agent
SET system_key = replace(system_key, 'upstream_sync_orphan:', 'upstream_sync:'), updated_at = now()
WHERE system_key LIKE 'upstream_sync_orphan:%';

ALTER TABLE upstream_sync_config DROP CONSTRAINT IF EXISTS upstream_sync_config_schedule_hour_range;
ALTER TABLE upstream_sync_config DROP COLUMN IF EXISTS schedule_hour;
ALTER TABLE upstream_sync_config DROP COLUMN IF EXISTS instructions;
