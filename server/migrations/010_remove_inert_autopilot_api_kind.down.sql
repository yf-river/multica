ALTER TABLE autopilot_trigger
    DROP CONSTRAINT autopilot_trigger_kind_check,
    ADD CONSTRAINT autopilot_trigger_kind_check
    CHECK (kind IN ('schedule', 'webhook', 'api'));

ALTER TABLE autopilot_run
    DROP CONSTRAINT autopilot_run_source_check,
    ADD CONSTRAINT autopilot_run_source_check
    CHECK (source IN ('schedule', 'manual', 'webhook', 'api'));
