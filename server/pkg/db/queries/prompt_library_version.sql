-- name: ListPromptLibraryVersions :many
SELECT * FROM prompt_library_version
WHERE workspace_id = $1
  AND prompt_id = $2
ORDER BY version DESC, created_at DESC;

-- name: GetPromptLibraryVersionInWorkspace :one
SELECT * FROM prompt_library_version
WHERE id = $1 AND workspace_id = $2;

-- name: CreatePromptLibraryVersion :one
INSERT INTO prompt_library_version (
    prompt_id, workspace_id, project_id, version, name, description, prompt_type,
    content, variables, tags, source, source_candidate_id, created_by
) VALUES (
    $1,
    $2,
    sqlc.narg('project_id'),
    $3,
    $4,
    $5,
    $6,
    $7,
    COALESCE(sqlc.narg('variables')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    $8,
    sqlc.narg('source_candidate_id'),
    $9
)
ON CONFLICT (prompt_id, version) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    prompt_type = EXCLUDED.prompt_type,
    content = EXCLUDED.content,
    variables = EXCLUDED.variables,
    tags = EXCLUDED.tags,
    source = EXCLUDED.source,
    source_candidate_id = EXCLUDED.source_candidate_id,
    created_by = COALESCE(EXCLUDED.created_by, prompt_library_version.created_by)
RETURNING *;
