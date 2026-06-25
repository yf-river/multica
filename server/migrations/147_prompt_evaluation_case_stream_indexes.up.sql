CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_case_stream_index
    ON prompt_evaluation_case(workspace_id, asset_id, status, source, case_index ASC, id ASC);

CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_case_stream_created
    ON prompt_evaluation_case(workspace_id, asset_id, status, source, created_at DESC, id ASC);

CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_case_stream_updated
    ON prompt_evaluation_case(workspace_id, asset_id, status, source, updated_at DESC, id ASC);

CREATE INDEX IF NOT EXISTS idx_prompt_evaluation_case_tags_gin
    ON prompt_evaluation_case USING GIN (tags);
