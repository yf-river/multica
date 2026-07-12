-- The current Issue contract has no acceptance_criteria or context_refs
-- request, response, UI, CLI, or service consumer. Remove these inert columns
-- instead of carrying their historical JSON shapes through every SELECT *.
ALTER TABLE issue
DROP COLUMN IF EXISTS acceptance_criteria,
DROP COLUMN IF EXISTS context_refs;
