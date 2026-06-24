DROP INDEX IF EXISTS idx_prompt_evaluation_run_workspace_reviewed;

ALTER TABLE prompt_evaluation_run
    DROP COLUMN IF EXISTS reviewed_at,
    DROP COLUMN IF EXISTS reviewed_by,
    DROP COLUMN IF EXISTS review_note,
    DROP COLUMN IF EXISTS review_decision;
