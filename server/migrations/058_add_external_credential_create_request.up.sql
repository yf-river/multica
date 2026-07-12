ALTER TABLE external_credential_profile
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_hash text
        CONSTRAINT external_credential_profile_request_hash_check
        CHECK (request_hash IS NULL OR request_hash ~ '^[0-9a-f]{64}$');

CREATE UNIQUE INDEX external_credential_profile_create_request_unique
    ON external_credential_profile (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
