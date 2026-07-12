ALTER TABLE webhook_delivery
    ADD COLUMN replay_actor_id uuid,
    ADD COLUMN replay_request_key uuid,
    ADD COLUMN replay_request_hash text;

ALTER TABLE webhook_delivery
    ADD CONSTRAINT webhook_delivery_replay_request_shape_check CHECK (
        (replay_actor_id IS NULL AND replay_request_key IS NULL AND replay_request_hash IS NULL)
        OR
        (replay_actor_id IS NOT NULL AND replay_request_key IS NOT NULL
         AND replay_request_hash ~ '^[0-9a-f]{64}$')
    );

CREATE UNIQUE INDEX webhook_delivery_replay_request_unique
    ON webhook_delivery (workspace_id, replay_actor_id, replay_request_key)
    WHERE replay_request_key IS NOT NULL;
