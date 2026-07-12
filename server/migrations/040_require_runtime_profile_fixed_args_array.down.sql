-- Normalization is intentionally one-way; rollback only removes the guard.
ALTER TABLE runtime_profile
DROP CONSTRAINT IF EXISTS runtime_profile_fixed_args_string_array;
