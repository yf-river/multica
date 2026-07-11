-- The old key referred to custom_env, which is no longer returned on Agent
-- resources. Normalize persisted settings to the one current secret-redaction
-- name while preserving all unrelated settings and an already-written new key.
UPDATE workspace
SET settings = jsonb_set(
    settings - 'always_redact_env',
    '{always_redact_secrets}',
    COALESCE(settings -> 'always_redact_secrets', settings -> 'always_redact_env'),
    true
)
WHERE settings ? 'always_redact_env';
