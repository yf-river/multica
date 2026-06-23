ALTER TABLE prompt_evaluation_dataset_row
    DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_source_check;

ALTER TABLE prompt_evaluation_dataset_row
    ADD CONSTRAINT prompt_evaluation_dataset_row_source_check
    CHECK (source IN ('payload', 'manual', 'trace'));
