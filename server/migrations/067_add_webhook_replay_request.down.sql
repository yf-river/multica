DROP INDEX IF EXISTS webhook_delivery_replay_request_unique;
ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_replay_request_shape_check;
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS replay_request_hash;
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS replay_request_key;
ALTER TABLE webhook_delivery DROP COLUMN IF EXISTS replay_actor_id;
