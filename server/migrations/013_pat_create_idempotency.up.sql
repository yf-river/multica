ALTER TABLE personal_access_token
  ADD COLUMN idempotency_key uuid,
  ADD COLUMN request_hash text;

CREATE UNIQUE INDEX personal_access_token_create_request_unique
  ON personal_access_token (user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
