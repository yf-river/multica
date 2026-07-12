-- Normalization is intentionally one-way; rollback only removes the guard.
ALTER TABLE squad
DROP CONSTRAINT IF EXISTS squad_sop_profile_object;
