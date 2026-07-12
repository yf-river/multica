ALTER TABLE prompt_library_trial
ADD COLUMN IF NOT EXISTS input text NOT NULL DEFAULT '';
