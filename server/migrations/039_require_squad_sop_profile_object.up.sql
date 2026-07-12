-- SOP profiles are keyed workflow documents in the current API. Normalize
-- retired non-object values once, then enforce the current shape at rest.
UPDATE squad
SET sop_profile = '{}'::jsonb
WHERE jsonb_typeof(sop_profile) IS DISTINCT FROM 'object';

ALTER TABLE squad
DROP CONSTRAINT IF EXISTS squad_sop_profile_object;

ALTER TABLE squad
ADD CONSTRAINT squad_sop_profile_object
CHECK (jsonb_typeof(sop_profile) = 'object');
