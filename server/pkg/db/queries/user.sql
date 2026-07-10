-- name: GetUser :one
SELECT * FROM "user"
WHERE id = $1;

-- name: GetUserByAccount :one
SELECT * FROM "user"
WHERE account = $1;

-- name: CreateUser :one
INSERT INTO "user" (name, account, avatar_url)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateUser :one
-- Patches the user-controlled profile fields. Each parameter follows
-- COALESCE-on-NULL semantics so the handler can omit any field it
-- doesn't intend to write.
--
-- `timezone` (Viewing-tz preference) participates in
-- the same shape but uses sqlc.narg + a sentinel-string convention:
-- the handler passes the empty string "" to mean "clear back to NULL"
-- (browser-detected fallback), an IANA name like "Asia/Shanghai" to
-- pin a value, and `sqlc.narg('timezone') IS NULL` (no value at all)
-- to leave the existing column untouched. Folding it into UpdateUser
-- rather than carrying a dedicated UpdateUserTimezone keeps the
-- profile-patch shape uniform between Preferences fields.
UPDATE "user" SET
    name = COALESCE($2, name),
    avatar_url = COALESCE($3, avatar_url),
    profile_description = COALESCE(sqlc.narg('profile_description'), profile_description),
    timezone = CASE
        WHEN sqlc.narg('timezone')::text IS NULL THEN timezone
        WHEN sqlc.narg('timezone')::text = ''    THEN NULL
        ELSE sqlc.narg('timezone')::text
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;
