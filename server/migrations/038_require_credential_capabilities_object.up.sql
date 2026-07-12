-- Credential capabilities are a keyed permission/feature document. Normalize
-- JSON null and other non-object values once, then enforce the shape already
-- required by Create/Update validation and Core response schemas.
UPDATE external_credential_profile
SET capabilities = '{}'::jsonb
WHERE jsonb_typeof(capabilities) IS DISTINCT FROM 'object';

ALTER TABLE external_credential_profile
DROP CONSTRAINT IF EXISTS external_credential_capabilities_object;

ALTER TABLE external_credential_profile
ADD CONSTRAINT external_credential_capabilities_object
CHECK (jsonb_typeof(capabilities) = 'object');
