-- custom_env and custom_args have one current shape each: a string-valued
-- object and a string array. Normalize values accepted by older direct writes
-- once, then enforce both the container and element types used by handlers,
-- daemon claims and Core schemas.
UPDATE agent
SET custom_env = '{}'::jsonb
WHERE jsonb_typeof(custom_env) IS DISTINCT FROM 'object'
   OR jsonb_path_exists(custom_env, '$.* ? (@.type() != "string")');

UPDATE agent
SET custom_args = '[]'::jsonb
WHERE jsonb_typeof(custom_args) IS DISTINCT FROM 'array'
   OR jsonb_path_exists(custom_args, '$[*] ? (@.type() != "string")');

ALTER TABLE agent
DROP CONSTRAINT IF EXISTS agent_custom_env_string_object,
DROP CONSTRAINT IF EXISTS agent_custom_args_string_array;

ALTER TABLE agent
ADD CONSTRAINT agent_custom_env_string_object CHECK (
    jsonb_typeof(custom_env) = 'object'
    AND NOT jsonb_path_exists(custom_env, '$.* ? (@.type() != "string")')
),
ADD CONSTRAINT agent_custom_args_string_array CHECK (
    jsonb_typeof(custom_args) = 'array'
    AND NOT jsonb_path_exists(custom_args, '$[*] ? (@.type() != "string")')
);
