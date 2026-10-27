CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_external_credential_profile_user_provider ON external_credential_profile(user_id, provider, updated_at DESC);
