-- Prompt Library clients use arrays for declared variables/tags and an object
-- for trial render values. Normalize retired invalid rows once, then make the
-- current shapes persistence invariants instead of response-time fallbacks.
UPDATE prompt_library_item
SET variables = '[]'::jsonb
WHERE jsonb_typeof(variables) IS DISTINCT FROM 'array';

UPDATE prompt_library_item
SET tags = '[]'::jsonb
WHERE jsonb_typeof(tags) IS DISTINCT FROM 'array';

UPDATE prompt_library_version
SET variables = '[]'::jsonb
WHERE jsonb_typeof(variables) IS DISTINCT FROM 'array';

UPDATE prompt_library_version
SET tags = '[]'::jsonb
WHERE jsonb_typeof(tags) IS DISTINCT FROM 'array';

UPDATE prompt_library_trial
SET variables = '{}'::jsonb
WHERE jsonb_typeof(variables) IS DISTINCT FROM 'object';

ALTER TABLE prompt_library_item
DROP CONSTRAINT IF EXISTS prompt_library_item_variables_array,
DROP CONSTRAINT IF EXISTS prompt_library_item_tags_array;

ALTER TABLE prompt_library_item
ADD CONSTRAINT prompt_library_item_variables_array CHECK (jsonb_typeof(variables) = 'array'),
ADD CONSTRAINT prompt_library_item_tags_array CHECK (jsonb_typeof(tags) = 'array');

ALTER TABLE prompt_library_version
DROP CONSTRAINT IF EXISTS prompt_library_version_variables_array,
DROP CONSTRAINT IF EXISTS prompt_library_version_tags_array;

ALTER TABLE prompt_library_version
ADD CONSTRAINT prompt_library_version_variables_array CHECK (jsonb_typeof(variables) = 'array'),
ADD CONSTRAINT prompt_library_version_tags_array CHECK (jsonb_typeof(tags) = 'array');

ALTER TABLE prompt_library_trial
DROP CONSTRAINT IF EXISTS prompt_library_trial_variables_object;

ALTER TABLE prompt_library_trial
ADD CONSTRAINT prompt_library_trial_variables_object CHECK (jsonb_typeof(variables) = 'object');
