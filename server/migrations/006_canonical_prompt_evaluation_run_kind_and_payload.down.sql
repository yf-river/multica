ALTER TABLE prompt_evaluation_run
    DROP CONSTRAINT prompt_evaluation_run_run_kind_check;

UPDATE prompt_evaluation_run
SET run_kind = '本地渲染'
WHERE run_kind = '模板渲染检查';

ALTER TABLE prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_run_kind_check
    CHECK (run_kind = ANY (ARRAY['本地渲染'::text, 'Agent执行'::text]));

UPDATE prompt_evaluation_asset
SET payload = payload || jsonb_build_object(
    '用例', COALESCE(payload->'cases', '[]'::jsonb),
    '语义版本', 'multica.training_evaluation.v1',
    'payload_contract', jsonb_build_object(
        'schema_version', 1,
        'schema', 'multica.training_evaluation.payload.v1',
        'cases', 'cases[].case_name / variables / expected_contains / tags',
        '写入策略', '新建和更新统一写入规范 cases。'
    )
)
WHERE payload->>'schema' = 'multica.training_evaluation.payload.v1';
