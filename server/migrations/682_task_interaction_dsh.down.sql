ALTER TABLE task_interaction DROP CONSTRAINT task_interaction_provider_check,
    ADD CONSTRAINT task_interaction_provider_check CHECK (provider IN ('codex', 'codebuddy'));
