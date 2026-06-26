UPDATE prompt_evaluation_case_assertion
SET status = CASE WHEN status = 'active' THEN '启用' ELSE '归档' END
WHERE status IN ('draft', 'approved', 'active');

UPDATE prompt_evaluation_case
SET status = CASE WHEN status = 'active' THEN '启用' ELSE '归档' END
WHERE status IN ('draft', 'approved', 'active');

ALTER TABLE prompt_evaluation_case_assertion
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_status_check,
    ADD CONSTRAINT prompt_evaluation_case_assertion_status_check
        CHECK (status IN ('启用', '归档'));

ALTER TABLE prompt_evaluation_case
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_status_check,
    ADD CONSTRAINT prompt_evaluation_case_status_check
        CHECK (status IN ('启用', '归档'));
