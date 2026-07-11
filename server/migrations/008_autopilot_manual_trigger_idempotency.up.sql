ALTER TABLE autopilot_run
    ADD COLUMN request_key uuid;

CREATE UNIQUE INDEX autopilot_run_request_key_unique
    ON autopilot_run (autopilot_id, source, request_key)
    WHERE request_key IS NOT NULL;
