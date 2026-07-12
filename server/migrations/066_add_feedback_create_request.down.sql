DROP INDEX IF EXISTS feedback_user_idempotency_key_idx;
ALTER TABLE feedback DROP CONSTRAINT IF EXISTS feedback_create_request_shape_check;
ALTER TABLE feedback DROP COLUMN IF EXISTS request_hash;
ALTER TABLE feedback DROP COLUMN IF EXISTS idempotency_key;
