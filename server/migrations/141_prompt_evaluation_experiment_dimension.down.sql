DROP TABLE IF EXISTS prompt_evaluation_experiment_dimension;

ALTER TABLE prompt_evaluation_asset
    DROP COLUMN IF EXISTS experiment_dimension_count;
