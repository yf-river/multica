CREATE TABLE prompt_evaluation_dimension_score (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    run_id UUID NOT NULL REFERENCES prompt_evaluation_run(id) ON DELETE CASCADE,
    asset_id UUID NOT NULL REFERENCES prompt_evaluation_asset(id) ON DELETE CASCADE,
    prompt_id UUID REFERENCES prompt_library_item(id) ON DELETE SET NULL,
    dimension_index INT NOT NULL DEFAULT 0,
    dimension_name TEXT NOT NULL DEFAULT '',
    score DOUBLE PRECISION NOT NULL DEFAULT 0,
    passed_cases INT NOT NULL DEFAULT 0,
    total_cases INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT '待执行' CHECK (status IN ('待执行', '已评分', '无用例')),
    rule TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'run_metrics' CHECK (source IN ('run_metrics', 'agent_sync', 'local_run')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT prompt_evaluation_dimension_score_run_dimension_unique UNIQUE (run_id, dimension_index, dimension_name)
);

CREATE INDEX idx_prompt_evaluation_dimension_score_workspace_created
    ON prompt_evaluation_dimension_score(workspace_id, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dimension_score_asset_dimension
    ON prompt_evaluation_dimension_score(asset_id, dimension_index ASC, created_at DESC);

CREATE INDEX idx_prompt_evaluation_dimension_score_prompt_dimension
    ON prompt_evaluation_dimension_score(prompt_id, dimension_index ASC, created_at DESC)
    WHERE prompt_id IS NOT NULL;

WITH run_scores AS (
    SELECT
        r.workspace_id,
        r.id AS run_id,
        r.asset_id,
        r.prompt_id,
        COALESCE((score.value->>'维度序号')::int, (score.value->>'dimension_index')::int, score.ordinality::int - 1) AS dimension_index,
        COALESCE(NULLIF(score.value->>'维度名称', ''), NULLIF(score.value->>'dimension_name', ''), '维度 ' || score.ordinality::text) AS dimension_name,
        COALESCE((score.value->>'得分')::double precision, (score.value->>'score')::double precision, 0) AS score,
        COALESCE((score.value->>'通过用例数')::int, (score.value->>'passed_cases')::int, 0) AS passed_cases,
        COALESCE((score.value->>'总用例数')::int, (score.value->>'total_cases')::int, 0) AS total_cases,
        COALESCE(NULLIF(score.value->>'状态', ''), NULLIF(score.value->>'status', ''), '待执行') AS status,
        COALESCE(NULLIF(score.value->>'评分规则', ''), NULLIF(score.value->>'rule', ''), '') AS rule,
        COALESCE(NULLIF(score.value->>'证据', ''), NULLIF(score.value->>'evidence', ''), '') AS evidence,
        CASE WHEN r.run_kind = 'Agent执行' AND r.status IN ('通过', '未通过', '失败', '需人工复核') THEN 'agent_sync' ELSE 'run_metrics' END AS source,
        r.created_at,
        r.updated_at
    FROM prompt_evaluation_run r
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(r.metrics->'实验维度评分') = 'array' THEN r.metrics->'实验维度评分'
            WHEN jsonb_typeof(r.metrics->'experiment_dimension_scores') = 'array' THEN r.metrics->'experiment_dimension_scores'
            WHEN jsonb_typeof(r.evidence->'实验维度评分') = 'array' THEN r.evidence->'实验维度评分'
            WHEN jsonb_typeof(r.evidence->'experiment_dimension_scores') = 'array' THEN r.evidence->'experiment_dimension_scores'
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS score(value, ordinality)
)
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
    source,
    created_at,
    updated_at
)
SELECT
    workspace_id,
    run_id,
    asset_id,
    prompt_id,
    dimension_index,
    dimension_name,
    score,
    passed_cases,
    total_cases,
    CASE WHEN status IN ('待执行', '已评分', '无用例') THEN status ELSE '待执行' END,
    rule,
    evidence,
    source,
    created_at,
    updated_at
FROM run_scores
WHERE dimension_name <> '';
