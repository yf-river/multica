DROP INDEX IF EXISTS idx_prompt_evaluation_case_operation_status_created;

ALTER TABLE prompt_evaluation_case_operation
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS error_message,
    DROP COLUMN IF EXISTS status;
