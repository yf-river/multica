-- name: ListPromptLibraryItems :many
SELECT * FROM prompt_library_item
WHERE workspace_id = $1
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('prompt_type')::text IS NULL OR prompt_type = sqlc.narg('prompt_type'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY updated_at DESC, created_at DESC;

-- name: GetPromptLibraryItemInWorkspace :one
SELECT * FROM prompt_library_item
WHERE id = $1 AND workspace_id = $2;

-- name: CreatePromptLibraryItem :one
INSERT INTO prompt_library_item (
    workspace_id, project_id, name, description, prompt_type,
    content, variables, tags, status, created_by
) VALUES (
    $1,
    sqlc.narg('project_id'),
    $2,
    $3,
    $4,
    $5,
    COALESCE(sqlc.narg('variables')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    $6
)
RETURNING *;

-- name: CreatePromptLibraryItemVersion :one
INSERT INTO prompt_library_item (
    workspace_id, project_id, name, description, prompt_type,
    content, variables, tags, status, version, created_by
) VALUES (
    $1,
    sqlc.narg('project_id'),
    $2,
    $3,
    $4,
    $5,
    COALESCE(sqlc.narg('variables')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    $6,
    $7
)
RETURNING *;

-- name: UpdatePromptLibraryItem :one
UPDATE prompt_library_item SET
    project_id = sqlc.narg('project_id'),
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    prompt_type = COALESCE(sqlc.narg('prompt_type'), prompt_type),
    content = COALESCE(sqlc.narg('content'), content),
    variables = COALESCE(sqlc.narg('variables')::jsonb, variables),
    tags = COALESCE(sqlc.narg('tags')::jsonb, tags),
    status = COALESCE(sqlc.narg('status'), status),
    version = version + 1,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeletePromptLibraryItem :exec
DELETE FROM prompt_library_item
WHERE id = $1 AND workspace_id = $2;
