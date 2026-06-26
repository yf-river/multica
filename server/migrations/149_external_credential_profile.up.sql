CREATE TABLE external_credential_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('tapd', 'gongfeng')),
    name TEXT NOT NULL,
    secret_ref TEXT NOT NULL DEFAULT '',
    encrypted_secret BYTEA,
    secret_hint TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified', 'verified', 'failed', 'disabled')),
    last_verified_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, provider, name),
    CHECK (secret_ref <> '' OR encrypted_secret IS NOT NULL)
);

CREATE INDEX idx_external_credential_profile_user_provider
    ON external_credential_profile(user_id, provider, created_at DESC);
