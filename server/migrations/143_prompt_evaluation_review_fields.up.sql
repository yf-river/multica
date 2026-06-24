ALTER TABLE prompt_evaluation_run
    ADD COLUMN review_decision TEXT NOT NULL DEFAULT ''
        CHECK (review_decision IN ('', '通过', '未通过')),
    ADD COLUMN review_note TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    ADD COLUMN reviewed_at TIMESTAMPTZ;

CREATE INDEX idx_prompt_evaluation_run_workspace_reviewed
    ON prompt_evaluation_run(workspace_id, reviewed_at DESC)
    WHERE reviewed_at IS NOT NULL;
