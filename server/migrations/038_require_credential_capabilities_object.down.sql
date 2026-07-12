-- The data normalization is intentionally one-way. Rolling back only removes
-- the object constraint; invalid capabilities cannot be reconstructed.
ALTER TABLE external_credential_profile
DROP CONSTRAINT IF EXISTS external_credential_capabilities_object;
