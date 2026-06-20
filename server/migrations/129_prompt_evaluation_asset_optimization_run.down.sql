ALTER TABLE prompt_evaluation_asset
    DROP CONSTRAINT prompt_evaluation_asset_type_check,
    ADD CONSTRAINT prompt_evaluation_asset_type_check
        CHECK (asset_type IN ('数据集', '测试套件', '实验'));
