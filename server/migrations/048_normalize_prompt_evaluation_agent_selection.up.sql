-- Agent selection is a top-level string `agent_id`. Normalize historical
-- object/top-level/nested aliases without removing unrelated nested metadata.
WITH candidates AS (
    SELECT
        id,
        payload,
        COALESCE(
            payload->'agent_id', payload->'执行智能体',
            payload->'execution_agent_id', payload->'target_agent_id',
            payload->'执行智能体标识', payload->'目标智能体标识',
            payload->'execution_agent', payload->'target_agent',
            payload#>'{历史调试载荷,执行智能体}',
            payload#>'{历史调试载荷,execution_agent}',
            payload#>'{历史调试载荷,agent_id}',
            payload#>'{历史调试载荷,execution_agent_id}',
            payload#>'{历史调试载荷,target_agent_id}',
            payload#>'{历史调试载荷,执行智能体标识}',
            payload#>'{历史调试载荷,目标智能体标识}',
            payload#>'{运行环境,执行智能体}',
            payload#>'{运行环境,execution_agent}',
            payload#>'{运行环境,agent_id}',
            payload#>'{运行环境,execution_agent_id}',
            payload#>'{运行环境,target_agent_id}',
            payload#>'{运行环境,执行智能体标识}',
            payload#>'{运行环境,目标智能体标识}'
        ) AS candidate
    FROM prompt_evaluation_asset
    WHERE payload ?| ARRAY[
        'agent_id', '执行智能体', 'execution_agent_id', 'target_agent_id',
        '执行智能体标识', '目标智能体标识', 'execution_agent', 'target_agent',
        '历史调试载荷', '运行环境'
    ]
), normalized AS (
    SELECT
        id,
        payload,
        CASE jsonb_typeof(candidate)
            WHEN 'string' THEN NULLIF(candidate #>> '{}', '')
            WHEN 'object' THEN NULLIF(COALESCE(
                candidate->>'agent_id', candidate->>'id',
                candidate->>'智能体标识', candidate->>'执行智能体标识'
            ), '')
            ELSE NULL
        END AS agent_id
    FROM candidates
)
UPDATE prompt_evaluation_asset AS asset
SET payload = (normalized.payload
        - '执行智能体' - 'execution_agent_id' - 'target_agent_id'
        - '执行智能体标识' - '目标智能体标识'
        - 'execution_agent' - 'target_agent')
    || jsonb_strip_nulls(jsonb_build_object('agent_id', to_jsonb(normalized.agent_id)))
FROM normalized
WHERE asset.id = normalized.id;

UPDATE prompt_evaluation_asset
SET payload = jsonb_set(
    payload,
    '{历史调试载荷}',
    (payload->'历史调试载荷')
        - '执行智能体' - 'execution_agent' - 'agent_id'
        - 'execution_agent_id' - 'target_agent_id'
        - '执行智能体标识' - '目标智能体标识',
    true
)
WHERE jsonb_typeof(payload->'历史调试载荷') = 'object';

UPDATE prompt_evaluation_asset
SET payload = jsonb_set(
    payload,
    '{运行环境}',
    (payload->'运行环境')
        - '执行智能体' - 'execution_agent' - 'agent_id'
        - 'execution_agent_id' - 'target_agent_id'
        - '执行智能体标识' - '目标智能体标识',
    true
)
WHERE jsonb_typeof(payload->'运行环境') = 'object';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'prompt_evaluation_asset_agent_id_string') THEN
        ALTER TABLE prompt_evaluation_asset
            ADD CONSTRAINT prompt_evaluation_asset_agent_id_string
            CHECK (NOT (payload ? 'agent_id') OR (
                jsonb_typeof(payload->'agent_id') = 'string'
                AND length(btrim(payload->>'agent_id')) > 0
            ));
    END IF;
END $$;
