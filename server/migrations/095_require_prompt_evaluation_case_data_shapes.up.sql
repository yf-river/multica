-- Cases, dataset rows and immutable version rows share one current JSON shape.
-- Normalize incompatible historical values once before enforcing that chain.
UPDATE prompt_evaluation_case SET
    variables = CASE WHEN jsonb_typeof(variables) = 'object' THEN variables ELSE '{}'::jsonb END,
    expected_contains = CASE WHEN jsonb_typeof(expected_contains) = 'array' THEN expected_contains ELSE '[]'::jsonb END,
    input = CASE WHEN jsonb_typeof(input) = 'object' THEN input ELSE '{}'::jsonb END,
    expected = CASE WHEN jsonb_typeof(expected) = 'object' THEN expected ELSE '{}'::jsonb END,
    tags = CASE WHEN jsonb_typeof(tags) = 'array' THEN tags ELSE '[]'::jsonb END;

UPDATE prompt_evaluation_dataset_row SET
    variables = CASE WHEN jsonb_typeof(variables) = 'object' THEN variables ELSE '{}'::jsonb END,
    expected_contains = CASE WHEN jsonb_typeof(expected_contains) = 'array' THEN expected_contains ELSE '[]'::jsonb END,
    expected = CASE WHEN jsonb_typeof(expected) = 'object' THEN expected ELSE '{}'::jsonb END,
    tags = CASE WHEN jsonb_typeof(tags) = 'array' THEN tags ELSE '[]'::jsonb END;

UPDATE prompt_evaluation_dataset_version SET
    metadata = CASE WHEN jsonb_typeof(metadata) = 'object' THEN metadata ELSE '{}'::jsonb END;

UPDATE prompt_evaluation_dataset_version_row SET
    variables = CASE WHEN jsonb_typeof(variables) = 'object' THEN variables ELSE '{}'::jsonb END,
    expected_contains = CASE WHEN jsonb_typeof(expected_contains) = 'array' THEN expected_contains ELSE '[]'::jsonb END,
    expected = CASE WHEN jsonb_typeof(expected) = 'object' THEN expected ELSE '{}'::jsonb END,
    tags = CASE WHEN jsonb_typeof(tags) = 'array' THEN tags ELSE '[]'::jsonb END;

ALTER TABLE prompt_evaluation_case
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_variables_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_expected_contains_is_array,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_input_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_expected_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_case_tags_is_array;

ALTER TABLE prompt_evaluation_case
ADD CONSTRAINT prompt_evaluation_case_variables_is_object CHECK (jsonb_typeof(variables) = 'object'),
ADD CONSTRAINT prompt_evaluation_case_expected_contains_is_array CHECK (jsonb_typeof(expected_contains) = 'array'),
ADD CONSTRAINT prompt_evaluation_case_input_is_object CHECK (jsonb_typeof(input) = 'object'),
ADD CONSTRAINT prompt_evaluation_case_expected_is_object CHECK (jsonb_typeof(expected) = 'object'),
ADD CONSTRAINT prompt_evaluation_case_tags_is_array CHECK (jsonb_typeof(tags) = 'array');

ALTER TABLE prompt_evaluation_dataset_row
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_variables_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_expected_contains_is_array,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_expected_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_row_tags_is_array;

ALTER TABLE prompt_evaluation_dataset_row
ADD CONSTRAINT prompt_evaluation_dataset_row_variables_is_object CHECK (jsonb_typeof(variables) = 'object'),
ADD CONSTRAINT prompt_evaluation_dataset_row_expected_contains_is_array CHECK (jsonb_typeof(expected_contains) = 'array'),
ADD CONSTRAINT prompt_evaluation_dataset_row_expected_is_object CHECK (jsonb_typeof(expected) = 'object'),
ADD CONSTRAINT prompt_evaluation_dataset_row_tags_is_array CHECK (jsonb_typeof(tags) = 'array');

ALTER TABLE prompt_evaluation_dataset_version
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_version_metadata_is_object;

ALTER TABLE prompt_evaluation_dataset_version
ADD CONSTRAINT prompt_evaluation_dataset_version_metadata_is_object CHECK (jsonb_typeof(metadata) = 'object');

ALTER TABLE prompt_evaluation_dataset_version_row
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_version_row_variables_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_version_row_expected_contains_is_array,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_version_row_expected_is_object,
DROP CONSTRAINT IF EXISTS prompt_evaluation_dataset_version_row_tags_is_array;

ALTER TABLE prompt_evaluation_dataset_version_row
ADD CONSTRAINT prompt_evaluation_dataset_version_row_variables_is_object CHECK (jsonb_typeof(variables) = 'object'),
ADD CONSTRAINT prompt_evaluation_dataset_version_row_expected_contains_is_array CHECK (jsonb_typeof(expected_contains) = 'array'),
ADD CONSTRAINT prompt_evaluation_dataset_version_row_expected_is_object CHECK (jsonb_typeof(expected) = 'object'),
ADD CONSTRAINT prompt_evaluation_dataset_version_row_tags_is_array CHECK (jsonb_typeof(tags) = 'array');
