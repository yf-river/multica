ALTER TABLE prompt_evaluation_run
    DROP CONSTRAINT prompt_evaluation_run_run_kind_check;

UPDATE prompt_evaluation_run
SET run_kind = '模板渲染检查'
WHERE run_kind = '本地渲染';

ALTER TABLE prompt_evaluation_run
    ADD CONSTRAINT prompt_evaluation_run_run_kind_check
    CHECK (run_kind = ANY (ARRAY['模板渲染检查'::text, 'Agent执行'::text]));

UPDATE prompt_evaluation_asset
SET payload = payload - '用例' - '语义版本' - 'payload_contract'
WHERE payload ?| ARRAY['用例', '语义版本', 'payload_contract'];
