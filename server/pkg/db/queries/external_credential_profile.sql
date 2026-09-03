-- name: CreateExternalCredentialProfile :one
INSERT INTO external_credential_profile (
    id, user_id, provider, name, secret_ref, encrypted_secret, secret_hint,
    capabilities, status, last_verified_at, last_error, idempotency_key, request_hash
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, COALESCE(sqlc.narg('capabilities')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('status')::text, 'unverified'),
    sqlc.narg('last_verified_at'),
    COALESCE(sqlc.narg('last_error')::text, ''),
    sqlc.narg('idempotency_key'), sqlc.narg('request_hash')
)
RETURNING *;

-- name: GetExternalCredentialProfileByCreateRequest :one
SELECT * FROM external_credential_profile
WHERE user_id = $1 AND idempotency_key = $2;

-- name: ListExternalCredentialProfilesByUser :many
SELECT * FROM external_credential_profile
WHERE user_id = $1
  AND (sqlc.narg('provider')::text IS NULL OR provider = sqlc.narg('provider'))
ORDER BY provider ASC, name ASC, created_at DESC;

-- name: GetExternalCredentialProfileForUser :one
SELECT * FROM external_credential_profile
WHERE id = $1 AND user_id = $2;

-- name: GetDefaultExternalCredentialProfileForUser :one
SELECT * FROM external_credential_profile
WHERE user_id = $1
  AND provider = $2
  AND status <> 'disabled'
ORDER BY
  CASE WHEN status = 'verified' THEN 0 WHEN status = 'unverified' THEN 1 ELSE 2 END,
  updated_at DESC
LIMIT 1;

-- name: UpdateExternalCredentialProfile :one
UPDATE external_credential_profile
SET
    name = COALESCE(sqlc.narg('name'), name),
    secret_ref = COALESCE(sqlc.narg('secret_ref'), secret_ref),
    encrypted_secret = CASE
        WHEN sqlc.narg('secret_ref') IS NOT NULL OR sqlc.narg('encrypted_secret')::bytea IS NOT NULL THEN sqlc.narg('encrypted_secret')::bytea
        ELSE encrypted_secret
    END,
    secret_hint = COALESCE(sqlc.narg('secret_hint'), secret_hint),
    capabilities = COALESCE(sqlc.narg('capabilities')::jsonb, capabilities),
    status = COALESCE(sqlc.narg('status'), status),
    last_verified_at = COALESCE(sqlc.narg('last_verified_at'), last_verified_at),
    last_error = COALESCE(sqlc.narg('last_error'), last_error),
    updated_at = now()
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id')
RETURNING *;

-- name: DeleteExternalCredentialProfile :exec
DELETE FROM external_credential_profile
WHERE id = $1 AND user_id = $2;
