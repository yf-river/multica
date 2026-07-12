-- Skill config is a keyed configuration/origin document in every current
-- caller and response schema. Normalize values accepted by the former untyped
-- request boundary once, then enforce that single persisted shape.
UPDATE skill
SET config = '{}'::jsonb
WHERE jsonb_typeof(config) IS DISTINCT FROM 'object';

ALTER TABLE skill
DROP CONSTRAINT IF EXISTS skill_config_object;

ALTER TABLE skill
ADD CONSTRAINT skill_config_object
CHECK (jsonb_typeof(config) = 'object');
