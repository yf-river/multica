-- Runtime profile fixed arguments have one current shape: non-empty strings.
-- Normalize retired/corrupt values once, then enforce the launch contract.
UPDATE runtime_profile
SET fixed_args = '[]'::jsonb
WHERE jsonb_typeof(fixed_args) IS DISTINCT FROM 'array'
   OR jsonb_path_exists(fixed_args, '$[*] ? (@.type() != "string" || @ like_regex "^\\s*$")');

ALTER TABLE runtime_profile
DROP CONSTRAINT IF EXISTS runtime_profile_fixed_args_string_array;

ALTER TABLE runtime_profile
ADD CONSTRAINT runtime_profile_fixed_args_string_array CHECK (
    jsonb_typeof(fixed_args) = 'array'
    AND NOT jsonb_path_exists(fixed_args, '$[*] ? (@.type() != "string" || @ like_regex "^\\s*$")')
);
