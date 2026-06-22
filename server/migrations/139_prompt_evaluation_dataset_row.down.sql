DROP TABLE IF EXISTS prompt_evaluation_dataset_row;

ALTER TABLE prompt_evaluation_asset
    DROP COLUMN IF EXISTS dataset_row_count;
