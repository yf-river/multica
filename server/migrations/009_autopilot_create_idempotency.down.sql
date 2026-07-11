DROP INDEX IF EXISTS autopilot_create_request_unique;

ALTER TABLE autopilot
    DROP CONSTRAINT IF EXISTS autopilot_initial_trigger_id_fkey,
    DROP CONSTRAINT IF EXISTS autopilot_request_identity_complete,
    DROP COLUMN IF EXISTS initial_trigger_id,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS request_key;
