ALTER TABLE prompt_evaluation_case
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_status_check,
    ADD CONSTRAINT prompt_evaluation_case_status_check
        CHECK (status IN ('启用', '归档', 'draft', 'approved', 'active'));

ALTER TABLE prompt_evaluation_case_assertion
    DROP CONSTRAINT IF EXISTS prompt_evaluation_case_assertion_status_check,
    ADD CONSTRAINT prompt_evaluation_case_assertion_status_check
        CHECK (status IN ('启用', '归档', 'draft', 'approved', 'active'));
