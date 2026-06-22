ALTER TABLE prompt_evaluation_run DROP CONSTRAINT IF EXISTS prompt_evaluation_run_status_check;
ALTER TABLE prompt_evaluation_run ADD CONSTRAINT prompt_evaluation_run_status_check
    CHECK (status IN ('已入队', '运行中', '通过', '未通过', '失败', '已取消', '需人工复核'));

ALTER TABLE prompt_evaluation_trial DROP CONSTRAINT IF EXISTS prompt_evaluation_trial_status_check;
ALTER TABLE prompt_evaluation_trial ADD CONSTRAINT prompt_evaluation_trial_status_check
    CHECK (status IN ('待执行', '通过', '未通过', '失败', '已跳过', '需人工复核'));
