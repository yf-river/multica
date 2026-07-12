-- Rollback restores empty columns only. Removed values cannot be reconstructed
-- because the current product has no semantic contract for either field.
ALTER TABLE issue
ADD COLUMN IF NOT EXISTS acceptance_criteria jsonb DEFAULT '[]'::jsonb NOT NULL,
ADD COLUMN IF NOT EXISTS context_refs jsonb DEFAULT '[]'::jsonb NOT NULL;
