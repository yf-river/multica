-- name: UpsertPromptEvaluationDimensionScore :one
INSERT INTO prompt_evaluation_dimension_score (
    workspace_id,
    run_id,
    asset_id,
    prompt_id,
    dimension_index,
    dimension_name,
    score,
    passed_cases,
    total_cases,
    status,
    rule,
    evidence,
    source
) VALUES (
    $1,
    $2,
    $3,
    sqlc.narg('prompt_id'),
    $4,
    $5,
    $6,
    $7,
    $8,
    $9,
    COALESCE(sqlc.narg('rule'), ''),
    COALESCE(sqlc.narg('evidence'), ''),
    $10
)
ON CONFLICT (run_id, dimension_index, dimension_name) DO UPDATE SET
    score = EXCLUDED.score,
    passed_cases = EXCLUDED.passed_cases,
    total_cases = EXCLUDED.total_cases,
    status = EXCLUDED.status,
    rule = EXCLUDED.rule,
    evidence = EXCLUDED.evidence,
    source = EXCLUDED.source,
    prompt_id = EXCLUDED.prompt_id,
    updated_at = now()
RETURNING *;

-- name: DeletePromptEvaluationDimensionScoresByRun :exec
DELETE FROM prompt_evaluation_dimension_score
WHERE workspace_id = $1 AND run_id = $2;

-- name: ListPromptEvaluationDimensionScores :many
SELECT * FROM prompt_evaluation_dimension_score
WHERE workspace_id = $1
  AND (sqlc.narg('run_id')::uuid IS NULL OR run_id = sqlc.narg('run_id'))
  AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
  AND (sqlc.narg('prompt_id')::uuid IS NULL OR prompt_id = sqlc.narg('prompt_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY created_at DESC, dimension_index ASC;

-- name: ListPromptEvaluationDimensionScoreSummaries :many
WITH filtered_scores AS (
    SELECT *
    FROM prompt_evaluation_dimension_score
    WHERE workspace_id = $1
      AND (sqlc.narg('asset_id')::uuid IS NULL OR asset_id = sqlc.narg('asset_id'))
      AND (sqlc.narg('prompt_id')::uuid IS NULL OR prompt_id = sqlc.narg('prompt_id'))
      AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
),
latest_scores AS (
    SELECT DISTINCT ON (asset_id, dimension_index, dimension_name)
        asset_id,
        dimension_index,
        dimension_name,
        status AS latest_status,
        rule AS latest_rule,
        evidence AS latest_evidence,
        source AS latest_source,
        updated_at AS latest_scored_at
    FROM filtered_scores
    ORDER BY asset_id, dimension_index, dimension_name, updated_at DESC, created_at DESC
)
SELECT
    f.workspace_id,
    f.asset_id,
    f.prompt_id,
    f.dimension_index,
    f.dimension_name,
    COUNT(*)::bigint AS run_count,
    COUNT(*) FILTER (WHERE f.status = '已评分')::bigint AS scored_run_count,
    COALESCE(SUM(f.passed_cases) FILTER (WHERE f.status = '已评分'), 0)::bigint AS passed_cases,
    COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0)::bigint AS total_cases,
    CASE
        WHEN COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0) > 0
            THEN COALESCE(SUM(f.passed_cases) FILTER (WHERE f.status = '已评分'), 0)::double precision
                / COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0)::double precision
        ELSE 0::double precision
    END AS score,
    l.latest_status,
    l.latest_rule,
    l.latest_evidence,
    l.latest_source,
    l.latest_scored_at
FROM filtered_scores f
JOIN latest_scores l
  ON l.asset_id = f.asset_id
 AND l.dimension_index = f.dimension_index
 AND l.dimension_name = f.dimension_name
GROUP BY
    f.workspace_id,
    f.asset_id,
    f.prompt_id,
    f.dimension_index,
    f.dimension_name,
    l.latest_status,
    l.latest_rule,
    l.latest_evidence,
    l.latest_source,
    l.latest_scored_at
ORDER BY f.asset_id, f.dimension_index ASC, f.dimension_name ASC;

-- name: ListPromptEvaluationDimensionScoreTrends :many
WITH filtered_scores AS (
    SELECT
        s.workspace_id,
        s.asset_id,
        s.prompt_id,
        s.dimension_index,
        s.dimension_name,
        s.status,
        s.passed_cases,
        s.total_cases,
        s.rule,
        s.evidence,
        s.source,
        s.updated_at,
        to_char(date_trunc('day', r.created_at), 'YYYY-MM-DD') AS period,
        CASE
            WHEN COALESCE(r.metrics->>'提示词版本', r.metrics->>'prompt_version', '') ~ '^[0-9]+$'
                THEN COALESCE(r.metrics->>'提示词版本', r.metrics->>'prompt_version')::int
            ELSE 0
        END AS prompt_version
    FROM prompt_evaluation_dimension_score s
    JOIN prompt_evaluation_run r
      ON r.id = s.run_id
     AND r.workspace_id = s.workspace_id
    WHERE s.workspace_id = $1
      AND (sqlc.narg('asset_id')::uuid IS NULL OR s.asset_id = sqlc.narg('asset_id'))
      AND (sqlc.narg('prompt_id')::uuid IS NULL OR s.prompt_id = sqlc.narg('prompt_id'))
      AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status'))
      AND (sqlc.narg('since')::timestamptz IS NULL OR r.created_at >= sqlc.narg('since'))
),
latest_scores AS (
    SELECT DISTINCT ON (asset_id, prompt_id, dimension_index, dimension_name, period, prompt_version)
        asset_id,
        prompt_id,
        dimension_index,
        dimension_name,
        period,
        prompt_version,
        status AS latest_status,
        rule AS latest_rule,
        evidence AS latest_evidence,
        source AS latest_source,
        updated_at AS latest_scored_at
    FROM filtered_scores
    ORDER BY asset_id, prompt_id, dimension_index, dimension_name, period, prompt_version, updated_at DESC
)
SELECT
    f.workspace_id,
    f.asset_id,
    f.prompt_id,
    f.dimension_index,
    f.dimension_name,
    f.period,
    f.prompt_version,
    COUNT(*)::bigint AS run_count,
    COUNT(*) FILTER (WHERE f.status = '已评分')::bigint AS scored_run_count,
    COALESCE(SUM(f.passed_cases) FILTER (WHERE f.status = '已评分'), 0)::bigint AS passed_cases,
    COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0)::bigint AS total_cases,
    CASE
        WHEN COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0) > 0
            THEN COALESCE(SUM(f.passed_cases) FILTER (WHERE f.status = '已评分'), 0)::double precision
                / COALESCE(SUM(f.total_cases) FILTER (WHERE f.status = '已评分'), 0)::double precision
        ELSE 0::double precision
    END AS score,
    l.latest_status,
    l.latest_rule,
    l.latest_evidence,
    l.latest_source,
    l.latest_scored_at
FROM filtered_scores f
JOIN latest_scores l
  ON l.asset_id = f.asset_id
 AND l.prompt_id IS NOT DISTINCT FROM f.prompt_id
 AND l.dimension_index = f.dimension_index
 AND l.dimension_name = f.dimension_name
 AND l.period = f.period
 AND l.prompt_version = f.prompt_version
GROUP BY
    f.workspace_id,
    f.asset_id,
    f.prompt_id,
    f.dimension_index,
    f.dimension_name,
    f.period,
    f.prompt_version,
    l.latest_status,
    l.latest_rule,
    l.latest_evidence,
    l.latest_source,
    l.latest_scored_at
ORDER BY f.period DESC, f.asset_id, f.dimension_index ASC, f.dimension_name ASC, f.prompt_version DESC;
