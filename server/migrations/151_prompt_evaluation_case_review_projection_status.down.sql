UPDATE prompt_evaluation_test_suite_case
SET status = CASE WHEN status = 'active' THEN '启用' ELSE '归档' END
WHERE status IN ('draft', 'approved', 'active');

UPDATE prompt_evaluation_dataset_row
SET status = CASE WHEN status = 'active' THEN '启用' ELSE '归档' END
WHERE status IN ('draft', 'approved', 'active');

ALTER TABLE prompt_evaluation_test_suite_case
    DROP CONSTRAINT IF EXISTS prompt_evaluation_test_suite_case_status_check,
    ADD CONSTRAINT prompt_evaluation_test_suite_case_status_check
        CHECK (status IN ('启用', '归档'));

ALTER TABLE prompt_evaluation_dataset_row
    DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_status_check,
    ADD CONSTRAINT prompt_evaluation_dataset_row_status_check
        CHECK (status IN ('启用', '归档'));
