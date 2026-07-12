ALTER TABLE feedback
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_hash text;

ALTER TABLE feedback
    ADD CONSTRAINT feedback_create_request_shape_check CHECK (
        (idempotency_key IS NULL AND request_hash IS NULL)
        OR
        (idempotency_key IS NOT NULL AND request_hash ~ '^[0-9a-f]{64}$')
    );

CREATE UNIQUE INDEX feedback_user_idempotency_key_idx
    ON feedback (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
