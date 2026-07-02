DELETE FROM prompt_evaluation_asset
WHERE asset_type IN ('实验', '优化运行');

DROP TABLE IF EXISTS prompt_evaluation_experiment_dimension;

ALTER TABLE prompt_evaluation_asset
    DROP CONSTRAINT IF EXISTS prompt_evaluation_asset_type_check,
    ADD CONSTRAINT prompt_evaluation_asset_type_check
        CHECK (asset_type IN ('数据集', '测试套件'));
