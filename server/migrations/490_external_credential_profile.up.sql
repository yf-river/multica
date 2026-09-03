CREATE TABLE IF NOT EXISTS external_credential_profile (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider IN ('tapd', 'gongfeng')),
    name text NOT NULL,
    secret_ref text NOT NULL DEFAULT '',
    encrypted_secret bytea,
    secret_hint text NOT NULL DEFAULT '',
    capabilities jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(capabilities) = 'object'),
    status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified', 'verified', 'failed', 'disabled')),
    last_verified_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    idempotency_key uuid,
    request_hash text,
    CHECK (secret_ref <> '' OR encrypted_secret IS NOT NULL)
);
