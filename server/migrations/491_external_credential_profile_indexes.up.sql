CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_external_credential_profile_user_idempotency ON external_credential_profile(user_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
