-- Convert persisted case objects to the single current payload contract.
-- Non-case payload metadata is preserved; malformed case entries are dropped,
-- matching the runtime's existing object-only case collection behavior.
WITH normalized AS (
    SELECT
        asset.id,
        COALESCE(
            jsonb_agg(
                jsonb_build_object(
                    'case_name', COALESCE(
                        NULLIF(entry.value->>'case_name', ''),
                        NULLIF(entry.value->>'name', ''),
                        NULLIF(entry.value->>'名称', ''),
                        NULLIF(entry.value->>'用例名称', ''),
                        '用例 ' || entry.ordinality::text
                    ),
                    'variables', CASE
                        WHEN jsonb_typeof(COALESCE(entry.value->'variables', entry.value->'变量', entry.value->'输入变量')) = 'object'
                            THEN COALESCE(entry.value->'variables', entry.value->'变量', entry.value->'输入变量')
                        ELSE '{}'::jsonb
                    END,
                    'expected_contains', CASE
                        WHEN jsonb_typeof(COALESCE(entry.value->'expected_contains', entry.value->'期望包含', entry.value->'期望')) = 'array'
                            THEN COALESCE(entry.value->'expected_contains', entry.value->'期望包含', entry.value->'期望')
                        WHEN jsonb_typeof(COALESCE(entry.value->'expected_contains', entry.value->'期望包含', entry.value->'期望')) = 'string'
                            THEN jsonb_build_array(COALESCE(entry.value->'expected_contains', entry.value->'期望包含', entry.value->'期望') #>> '{}')
                        ELSE '[]'::jsonb
                    END,
                    'tags', CASE
                        WHEN jsonb_typeof(COALESCE(entry.value->'tags', entry.value->'标签')) = 'array'
                            THEN COALESCE(entry.value->'tags', entry.value->'标签')
                        WHEN jsonb_typeof(COALESCE(entry.value->'tags', entry.value->'标签')) = 'string'
                            THEN jsonb_build_array(COALESCE(entry.value->'tags', entry.value->'标签') #>> '{}')
                        ELSE '[]'::jsonb
                    END
                ) ORDER BY entry.ordinality
            ) FILTER (WHERE jsonb_typeof(entry.value) = 'object'),
            '[]'::jsonb
        ) AS cases
    FROM prompt_evaluation_asset AS asset
    CROSS JOIN LATERAL jsonb_array_elements(asset.payload->'cases') WITH ORDINALITY AS entry(value, ordinality)
    WHERE jsonb_typeof(asset.payload->'cases') = 'array'
    GROUP BY asset.id
)
UPDATE prompt_evaluation_asset AS asset
SET payload = jsonb_set(asset.payload, '{cases}', normalized.cases, true)
FROM normalized
WHERE asset.id = normalized.id;
