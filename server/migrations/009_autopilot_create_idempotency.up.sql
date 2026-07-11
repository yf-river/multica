ALTER TABLE autopilot
    ADD COLUMN request_key uuid,
    ADD COLUMN request_hash text,
    ADD COLUMN initial_trigger_id uuid,
    ADD CONSTRAINT autopilot_request_identity_complete CHECK (
        (request_key IS NULL AND request_hash IS NULL)
        OR
        (request_key IS NOT NULL AND request_hash ~ '^[0-9a-f]{64}$')
    );

ALTER TABLE autopilot
    ADD CONSTRAINT autopilot_initial_trigger_id_fkey
    FOREIGN KEY (initial_trigger_id)
    REFERENCES autopilot_trigger(id)
    ON DELETE SET NULL;

CREATE UNIQUE INDEX autopilot_create_request_unique
    ON autopilot (workspace_id, created_by_type, created_by_id, request_key)
    WHERE request_key IS NOT NULL;
