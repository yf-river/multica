DROP INDEX IF EXISTS autopilot_run_request_key_unique;

ALTER TABLE autopilot_run
    DROP COLUMN IF EXISTS request_key;
