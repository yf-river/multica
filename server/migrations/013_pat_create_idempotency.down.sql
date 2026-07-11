DROP INDEX IF EXISTS personal_access_token_create_request_unique;

ALTER TABLE personal_access_token
  DROP COLUMN IF EXISTS request_hash,
  DROP COLUMN IF EXISTS idempotency_key;
