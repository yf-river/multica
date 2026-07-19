ALTER TABLE autopilot
    ADD COLUMN request_key uuid,
    ADD COLUMN request_hash text,
    ADD COLUMN initial_trigger_id uuid,
    ADD CONSTRAINT autopilot_request_identity_complete
        CHECK ((request_key IS NULL AND request_hash IS NULL)
            OR (request_key IS NOT NULL AND request_hash ~ '^[0-9a-f]{64}$')),
    ADD CONSTRAINT autopilot_initial_trigger_id_fkey
        FOREIGN KEY (initial_trigger_id) REFERENCES autopilot_trigger(id) ON DELETE SET NULL;

ALTER TABLE autopilot_run
    ADD COLUMN request_key uuid;

ALTER TABLE external_credential_profile
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_hash text,
    ADD CONSTRAINT external_credential_profile_request_hash_check
        CHECK (request_hash IS NULL OR request_hash ~ '^[0-9a-f]{64}$');

ALTER TABLE feedback
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_hash text,
    ADD CONSTRAINT feedback_create_request_shape_check
        CHECK ((idempotency_key IS NULL AND request_hash IS NULL)
            OR (idempotency_key IS NOT NULL AND request_hash ~ '^[0-9a-f]{64}$'));

ALTER TABLE personal_access_token
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_hash text;

ALTER TABLE webhook_delivery
    ADD COLUMN replay_actor_id uuid,
    ADD COLUMN replay_request_key uuid,
    ADD COLUMN replay_request_hash text,
    ADD CONSTRAINT webhook_delivery_replay_request_shape_check
        CHECK ((replay_actor_id IS NULL AND replay_request_key IS NULL AND replay_request_hash IS NULL)
            OR (replay_actor_id IS NOT NULL AND replay_request_key IS NOT NULL
                AND replay_request_hash ~ '^[0-9a-f]{64}$'));

CREATE UNIQUE INDEX autopilot_create_request_unique
    ON autopilot (workspace_id, created_by_type, created_by_id, request_key)
    WHERE request_key IS NOT NULL;
CREATE UNIQUE INDEX autopilot_run_request_key_unique
    ON autopilot_run (autopilot_id, source, request_key)
    WHERE request_key IS NOT NULL;
CREATE UNIQUE INDEX external_credential_profile_create_request_unique
    ON external_credential_profile (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX feedback_user_idempotency_key_idx
    ON feedback (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX personal_access_token_create_request_unique
    ON personal_access_token (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX webhook_delivery_replay_request_unique
    ON webhook_delivery (workspace_id, replay_actor_id, replay_request_key)
    WHERE replay_request_key IS NOT NULL;

WITH duplicates AS (
    SELECT id,
        row_number() OVER (
            PARTITION BY issue_id, actor_id, details->>'task_id'
            ORDER BY created_at, id
        ) AS ordinal
    FROM activity_log
    WHERE actor_type = 'agent' AND action = 'squad_leader_evaluated'
)
DELETE FROM activity_log
WHERE id IN (SELECT id FROM duplicates WHERE ordinal > 1);

DROP INDEX idx_issue_origin;
CREATE UNIQUE INDEX idx_issue_origin
    ON issue (workspace_id, origin_type, origin_id)
    WHERE origin_type IS NOT NULL;

CREATE UNIQUE INDEX activity_log_squad_evaluation_task_unique
    ON activity_log (issue_id, actor_id, (details->>'task_id'))
    WHERE actor_type = 'agent' AND action = 'squad_leader_evaluated';
CREATE UNIQUE INDEX idx_squad_sop_terminal_event_task_unique
    ON squad_sop_step_event (run_id, task_id, event_type)
    WHERE task_id IS NOT NULL AND created_by_type = 'system'
        AND event_type IN ('步骤完成', '步骤失败');

DROP INDEX idx_task_message_task_id_seq;
CREATE UNIQUE INDEX idx_task_message_task_id_seq ON task_message (task_id, seq);
