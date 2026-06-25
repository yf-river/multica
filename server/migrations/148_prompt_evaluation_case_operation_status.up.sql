ALTER TABLE prompt_evaluation_case_operation
    ADD COLUMN status TEXT NOT NULL DEFAULT '已完成',
    ADD COLUMN error_message TEXT NOT NULL DEFAULT '',
    ADD COLUMN started_at TIMESTAMPTZ,
    ADD COLUMN completed_at TIMESTAMPTZ,
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX idx_prompt_evaluation_case_operation_status_created
    ON prompt_evaluation_case_operation(workspace_id, status, created_at DESC);
