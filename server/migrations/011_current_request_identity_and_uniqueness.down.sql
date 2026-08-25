DROP INDEX IF EXISTS idx_task_message_task_id_seq;
CREATE INDEX idx_task_message_task_id_seq ON task_message (task_id, seq);
DROP INDEX IF EXISTS idx_squad_sop_terminal_event_task_unique;
DROP INDEX IF EXISTS activity_log_squad_evaluation_task_unique;
DROP INDEX IF EXISTS idx_issue_origin;
CREATE INDEX idx_issue_origin ON issue (origin_type, origin_id) WHERE origin_type IS NOT NULL;
DROP INDEX IF EXISTS webhook_delivery_replay_request_unique;
DROP INDEX IF EXISTS personal_access_token_create_request_unique;
DROP INDEX IF EXISTS feedback_user_idempotency_key_idx;
DROP INDEX IF EXISTS external_credential_profile_create_request_unique;
DROP INDEX IF EXISTS autopilot_run_request_key_unique;
DROP INDEX IF EXISTS autopilot_create_request_unique;

ALTER TABLE webhook_delivery
    DROP CONSTRAINT IF EXISTS webhook_delivery_replay_request_shape_check,
    DROP COLUMN IF EXISTS replay_request_hash,
    DROP COLUMN IF EXISTS replay_request_key,
    DROP COLUMN IF EXISTS replay_actor_id;
ALTER TABLE personal_access_token
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE feedback
    DROP CONSTRAINT IF EXISTS feedback_create_request_shape_check,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE external_credential_profile
    DROP CONSTRAINT IF EXISTS external_credential_profile_request_hash_check,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE autopilot_run DROP COLUMN IF EXISTS request_key;
ALTER TABLE autopilot
    DROP CONSTRAINT IF EXISTS autopilot_initial_trigger_id_fkey,
    DROP CONSTRAINT IF EXISTS autopilot_request_identity_complete,
    DROP COLUMN IF EXISTS initial_trigger_id,
    DROP COLUMN IF EXISTS request_hash,
    DROP COLUMN IF EXISTS request_key;
