ALTER TABLE issue_review ADD COLUMN IF NOT EXISTS input_version text NOT NULL DEFAULT '';
