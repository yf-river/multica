-- Normalization is intentionally one-way; rollback only removes the guard.
ALTER TABLE agent_playground_input
DROP CONSTRAINT IF EXISTS agent_playground_input_variables_object;
