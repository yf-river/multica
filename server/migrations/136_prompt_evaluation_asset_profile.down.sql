ALTER TABLE prompt_evaluation_asset
    DROP COLUMN IF EXISTS evaluation_dimension_count,
    DROP COLUMN IF EXISTS linked_prompt_count,
    DROP COLUMN IF EXISTS linked_dataset_count,
    DROP COLUMN IF EXISTS structured_assertion_count,
    DROP COLUMN IF EXISTS structured_variable_count,
    DROP COLUMN IF EXISTS structured_case_count,
    DROP COLUMN IF EXISTS structure_schema;
