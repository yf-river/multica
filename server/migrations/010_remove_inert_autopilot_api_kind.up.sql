DELETE FROM autopilot_trigger
WHERE kind = 'api';

DELETE FROM autopilot_run
WHERE source = 'api';

ALTER TABLE autopilot_trigger
    DROP CONSTRAINT autopilot_trigger_kind_check,
    ADD CONSTRAINT autopilot_trigger_kind_check
    CHECK (kind IN ('schedule', 'webhook'));

ALTER TABLE autopilot_run
    DROP CONSTRAINT autopilot_run_source_check,
    ADD CONSTRAINT autopilot_run_source_check
    CHECK (source IN ('schedule', 'manual', 'webhook'));
