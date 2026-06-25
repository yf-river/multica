-- name: ListPromptEvaluationCases :many
SELECT * FROM prompt_evaluation_case
WHERE workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source'))
  AND (
    sqlc.narg('tag')::text IS NULL
    OR tags ? sqlc.narg('tag')::text
  )
  AND (
    sqlc.narg('keyword')::text IS NULL
    OR case_name ILIKE '%' || sqlc.narg('keyword') || '%'
    OR status ILIKE '%' || sqlc.narg('keyword') || '%'
    OR source ILIKE '%' || sqlc.narg('keyword') || '%'
    OR variables::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR expected_contains::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR input::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR expected::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR tags::text ILIKE '%' || sqlc.narg('keyword') || '%'
  )
  AND (
    sqlc.narg('cursor_id')::uuid IS NULL
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_index'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc'
      AND (
        case_index > sqlc.narg('cursor_case_index')::int
        OR (case_index = sqlc.narg('cursor_case_index')::int AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_index'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc'
      AND (
        case_index < sqlc.narg('cursor_case_index')::int
        OR (case_index = sqlc.narg('cursor_case_index')::int AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_name'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc'
      AND (
        case_name > sqlc.narg('cursor_case_name')::text
        OR (case_name = sqlc.narg('cursor_case_name')::text AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_name'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc'
      AND (
        case_name < sqlc.narg('cursor_case_name')::text
        OR (case_name = sqlc.narg('cursor_case_name')::text AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'source'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc'
      AND (
        source > sqlc.narg('cursor_source')::text
        OR (source = sqlc.narg('cursor_source')::text AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'source'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc'
      AND (
        source < sqlc.narg('cursor_source')::text
        OR (source = sqlc.narg('cursor_source')::text AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'created_at'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc'
      AND (
        created_at > sqlc.narg('cursor_created_at')::timestamptz
        OR (created_at = sqlc.narg('cursor_created_at')::timestamptz AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'created_at'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc'
      AND (
        created_at < sqlc.narg('cursor_created_at')::timestamptz
        OR (created_at = sqlc.narg('cursor_created_at')::timestamptz AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'updated_at'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc'
      AND (
        updated_at > sqlc.narg('cursor_updated_at')::timestamptz
        OR (updated_at = sqlc.narg('cursor_updated_at')::timestamptz AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
    OR (
      COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'updated_at'
      AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc'
      AND (
        updated_at < sqlc.narg('cursor_updated_at')::timestamptz
        OR (updated_at = sqlc.narg('cursor_updated_at')::timestamptz AND id > sqlc.narg('cursor_id')::uuid)
      )
    )
  )
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_index' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc' THEN case_index END ASC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_index' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc' THEN case_index END DESC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_name' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc' THEN case_name END ASC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'case_name' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc' THEN case_name END DESC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'source' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc' THEN source END ASC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'source' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc' THEN source END DESC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'created_at' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc' THEN created_at END ASC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'created_at' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc' THEN created_at END DESC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'updated_at' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'asc' THEN updated_at END ASC,
  CASE WHEN COALESCE(sqlc.narg('sort_by')::text, 'case_index') = 'updated_at' AND COALESCE(sqlc.narg('sort_direction')::text, 'asc') = 'desc' THEN updated_at END DESC,
  id ASC
LIMIT COALESCE(sqlc.narg('limit')::int, 5000);

-- name: CountPromptEvaluationCases :one
SELECT count(*) FROM prompt_evaluation_case
WHERE workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('source')::text IS NULL OR source = sqlc.narg('source'))
  AND (
    sqlc.narg('tag')::text IS NULL
    OR tags ? sqlc.narg('tag')::text
  )
  AND (
    sqlc.narg('keyword')::text IS NULL
    OR case_name ILIKE '%' || sqlc.narg('keyword') || '%'
    OR status ILIKE '%' || sqlc.narg('keyword') || '%'
    OR source ILIKE '%' || sqlc.narg('keyword') || '%'
    OR variables::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR expected_contains::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR input::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR expected::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR tags::text ILIKE '%' || sqlc.narg('keyword') || '%'
  );

-- name: ListPromptEvaluationCaseTagSummaries :many
SELECT tag, count(*)::int AS case_count
FROM prompt_evaluation_case c
CROSS JOIN LATERAL jsonb_array_elements_text(c.tags) AS tag
WHERE c.workspace_id = $1
  AND (sqlc.narg('asset_id')::uuid IS NULL OR c.asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status'))
  AND (sqlc.narg('source')::text IS NULL OR c.source = sqlc.narg('source'))
  AND btrim(tag) <> ''
  AND (
    sqlc.narg('keyword')::text IS NULL
    OR c.case_name ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.status ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.source ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.variables::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.expected_contains::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.input::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.expected::text ILIKE '%' || sqlc.narg('keyword') || '%'
    OR c.tags::text ILIKE '%' || sqlc.narg('keyword') || '%'
  )
GROUP BY tag
ORDER BY case_count DESC, tag ASC
LIMIT COALESCE(sqlc.narg('limit')::int, 50);

-- name: ListPromptEvaluationCaseTagDatasetSummaries :many
WITH tag_assets AS (
  SELECT
    tag,
    c.asset_id,
    a.name AS asset_name,
    count(*)::int AS case_count
  FROM prompt_evaluation_case c
  JOIN prompt_evaluation_asset a
    ON a.workspace_id = c.workspace_id
   AND a.id = c.asset_id
   AND a.asset_type = '数据集'
  CROSS JOIN LATERAL jsonb_array_elements_text(c.tags) AS tag
  WHERE c.workspace_id = $1
    AND (sqlc.narg('status')::text IS NULL OR c.status = sqlc.narg('status'))
    AND (sqlc.narg('source')::text IS NULL OR c.source = sqlc.narg('source'))
    AND btrim(tag) <> ''
    AND (
      sqlc.narg('keyword')::text IS NULL
      OR c.case_name ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.status ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.source ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.variables::text ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.expected_contains::text ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.input::text ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.expected::text ILIKE '%' || sqlc.narg('keyword') || '%'
      OR c.tags::text ILIKE '%' || sqlc.narg('keyword') || '%'
    )
  GROUP BY tag, c.asset_id, a.name
),
top_tags AS (
  SELECT tag
  FROM tag_assets
  GROUP BY tag
  ORDER BY sum(case_count) DESC, tag ASC
  LIMIT COALESCE(sqlc.narg('limit')::int, 20)
),
ranked AS (
  SELECT
    ta.tag,
    ta.asset_id,
    ta.asset_name,
    ta.case_count,
    sum(ta.case_count) OVER (PARTITION BY ta.tag)::int AS total_case_count,
    count(*) OVER (PARTITION BY ta.tag)::int AS dataset_count,
    row_number() OVER (PARTITION BY ta.tag ORDER BY ta.case_count DESC, ta.asset_name ASC, ta.asset_id ASC) AS dataset_rank
  FROM tag_assets ta
  JOIN top_tags tt ON tt.tag = ta.tag
)
SELECT tag, total_case_count, dataset_count, asset_id, asset_name, case_count
FROM ranked
WHERE dataset_rank <= COALESCE(sqlc.narg('top_dataset_limit')::int, 3)
ORDER BY total_case_count DESC, tag ASC, dataset_rank ASC;

-- name: GetPromptEvaluationCaseInWorkspace :one
SELECT * FROM prompt_evaluation_case
WHERE id = $1 AND workspace_id = $2;

-- name: DeletePromptEvaluationCasesByAsset :exec
DELETE FROM prompt_evaluation_case
WHERE workspace_id = $1 AND asset_id = $2;

-- name: DeletePromptEvaluationPayloadCasesByAsset :exec
DELETE FROM prompt_evaluation_case
WHERE workspace_id = $1 AND asset_id = $2 AND source = 'payload';

-- name: CreatePromptEvaluationCase :one
INSERT INTO prompt_evaluation_case (
    workspace_id,
    asset_id,
    prompt_id,
    case_index,
    case_name,
    variables,
    expected_contains,
    input,
    expected,
    tags,
    status,
    source,
    created_by
) VALUES (
    $1,
    $2,
    sqlc.narg('prompt_id'),
    $3,
    COALESCE(sqlc.narg('case_name'), ''),
    COALESCE(sqlc.narg('variables')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected_contains')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('input')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('expected')::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg('tags')::jsonb, '[]'::jsonb),
    COALESCE(sqlc.narg('status'), '启用'),
    COALESCE(sqlc.narg('source'), 'payload'),
    sqlc.narg('created_by')
)
RETURNING *;

-- name: UpdatePromptEvaluationCase :one
UPDATE prompt_evaluation_case SET
    asset_id = $3,
    prompt_id = sqlc.narg('prompt_id'),
    case_index = $4,
    case_name = $5,
    variables = COALESCE(sqlc.narg('variables')::jsonb, variables),
    expected_contains = COALESCE(sqlc.narg('expected_contains')::jsonb, expected_contains),
    input = COALESCE(sqlc.narg('input')::jsonb, input),
    expected = COALESCE(sqlc.narg('expected')::jsonb, expected),
    tags = COALESCE(sqlc.narg('tags')::jsonb, tags),
    status = $6,
    source = $7,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeletePromptEvaluationCase :exec
DELETE FROM prompt_evaluation_case
WHERE id = $1 AND workspace_id = $2;
