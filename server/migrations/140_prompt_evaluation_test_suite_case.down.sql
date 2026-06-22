DROP TABLE IF EXISTS prompt_evaluation_test_suite_case;

ALTER TABLE prompt_evaluation_asset
    DROP COLUMN IF EXISTS test_suite_case_count;
